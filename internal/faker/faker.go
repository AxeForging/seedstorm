package faker

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/brianvoe/gofakeit/v6"
)

// Generate produces fake data rows for each table, respecting FK ordering.
// If db is non-nil, existing PKs are read so FKs can reference them.
func Generate(s *schema.Schema, sortedTables []string, rows, enumRows int, db *sql.DB) (map[string][]map[string]interface{}, error) {
	data := make(map[string][]map[string]interface{})
	generatedPKs := make(map[string][]interface{})

	if db != nil {
		if err := queryExistingPKs(db, sortedTables, s.Tables, generatedPKs); err != nil {
			return nil, err
		}
	}

	for _, tableName := range sortedTables {
		table := s.Tables[tableName]
		data[tableName] = nil

		enumCol, enumVals := findEnumColumn(table)

		if enumCol != "" && enumRows > 0 {
			if err := generateEnumRows(data, generatedPKs, table, tableName, enumCol, enumVals, enumRows); err != nil {
				return nil, fmt.Errorf("table %s: %w", tableName, err)
			}
		} else {
			if err := generateStandardRows(data, generatedPKs, table, tableName, rows); err != nil {
				return nil, fmt.Errorf("table %s: %w", tableName, err)
			}
		}
	}

	return data, nil
}

func queryExistingPKs(db *sql.DB, sortedTables []string, tables map[string]schema.Table, generatedPKs map[string][]interface{}) error {
	for _, tableName := range sortedTables {
		table := tables[tableName]
		for colName, col := range table.Columns {
			if !col.PK {
				continue
			}
			rows, err := db.Query(fmt.Sprintf("SELECT %s FROM %s", colName, tableName)) //nolint:gosec
			if err != nil {
				return fmt.Errorf("failed to query PKs for %s.%s: %w", tableName, colName, err)
			}
			defer rows.Close()
			for rows.Next() {
				var pk interface{}
				if err := rows.Scan(&pk); err != nil {
					return err
				}
				generatedPKs[tableName] = append(generatedPKs[tableName], pk)
			}
		}
	}
	return nil
}

func findEnumColumn(table schema.Table) (string, []string) {
	for colName, col := range table.Columns {
		if strings.HasPrefix(col.Faker, "randomstring(") {
			re := regexp.MustCompile(`\(([^)]+)\)`)
			if m := re.FindStringSubmatch(col.Faker); len(m) > 1 {
				return colName, strings.Split(m[1], ",")
			}
		}
	}
	return "", nil
}

func generateEnumRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName, enumCol string, enumVals []string, enumRows int) error {
	for _, enumVal := range enumVals {
		v := enumVal
		for i := 0; i < enumRows; i++ {
			row, err := generateRow(table, tableName, generatedPKs, &v, enumCol)
			if err != nil {
				return err
			}
			data[tableName] = append(data[tableName], row)
		}
	}
	return nil
}

func generateStandardRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int) error {
	for i := 0; i < rows; i++ {
		row, err := generateRow(table, tableName, generatedPKs, nil, "")
		if err != nil {
			return err
		}
		data[tableName] = append(data[tableName], row)
	}
	return nil
}

func generateRow(table schema.Table, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string) (map[string]interface{}, error) {
	row := make(map[string]interface{})
	for colName, col := range table.Columns {
		val, err := generateValue(col, colName, tableName, generatedPKs, enumVal, enumCol)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", colName, err)
		}
		row[colName] = val
		if col.PK {
			generatedPKs[tableName] = append(generatedPKs[tableName], val)
		}
	}
	return row, nil
}

func generateValue(col schema.Column, colName, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string) (interface{}, error) {
	if enumVal != nil && colName == enumCol {
		return *enumVal, nil
	}
	if col.PK {
		return len(generatedPKs[tableName]) + 1, nil
	}
	if col.FK != "" {
		parts := strings.SplitN(col.FK, ".", 2)
		if len(parts) == 2 {
			fkTable := parts[0]
			if pks, ok := generatedPKs[fkTable]; ok && len(pks) > 0 {
				return pks[gofakeit.Number(0, len(pks)-1)], nil
			}
			return nil, fmt.Errorf("no PKs available for FK table %s", fkTable)
		}
	}
	return generate(col.Faker)
}

var reArgs = regexp.MustCompile(`^(\w+)\(([^)]*)\)$`)

func generate(fakerStr string) (interface{}, error) {
	s := strings.TrimSpace(fakerStr)

	// Special cases that return non-string values
	if m := reArgs.FindStringSubmatch(s); m != nil {
		funcName := m[1]
		argsStr := strings.TrimSpace(m[2])
		args := splitArgs(argsStr)

		switch funcName {
		case "number":
			min, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil {
				return nil, fmt.Errorf("number: bad min arg: %w", err)
			}
			max, err := strconv.Atoi(strings.TrimSpace(args[1]))
			if err != nil {
				return nil, fmt.Errorf("number: bad max arg: %w", err)
			}
			return gofakeit.Number(min, max), nil
		case "price":
			min, err := strconv.ParseFloat(strings.TrimSpace(args[0]), 64)
			if err != nil {
				return nil, fmt.Errorf("price: bad min arg: %w", err)
			}
			max, err := strconv.ParseFloat(strings.TrimSpace(args[1]), 64)
			if err != nil {
				return nil, fmt.Errorf("price: bad max arg: %w", err)
			}
			return gofakeit.Price(min, max), nil
		case "randomstring":
			return gofakeit.RandomString(args), nil
		case "paragraph":
			count := 1
			if len(args) > 0 {
				if n, err := strconv.Atoi(strings.TrimSpace(args[0])); err == nil {
					count = n
				}
			}
			return gofakeit.Paragraph(count, 3, 8, " "), nil
		case "float64":
			return gofakeit.Float64(), nil
		}
	}

	switch s {
	case "name":
		return gofakeit.Name(), nil
	case "firstname":
		return gofakeit.FirstName(), nil
	case "lastname":
		return gofakeit.LastName(), nil
	case "username":
		return gofakeit.Username(), nil
	case "email":
		return gofakeit.Email(), nil
	case "phone":
		return gofakeit.Phone(), nil
	case "street":
		return gofakeit.Street(), nil
	case "city":
		return gofakeit.City(), nil
	case "state":
		return gofakeit.State(), nil
	case "country":
		return gofakeit.Country(), nil
	case "zip":
		return gofakeit.Zip(), nil
	case "url":
		return gofakeit.URL(), nil
	case "uuid":
		return gofakeit.UUID(), nil
	case "ipv4":
		return gofakeit.IPv4Address(), nil
	case "macaddress":
		return gofakeit.MacAddress(), nil
	case "hexcolor":
		return gofakeit.HexColor(), nil
	case "company":
		return gofakeit.Company(), nil
	case "jobtitle":
		return gofakeit.JobTitle(), nil
	case "latitude":
		return gofakeit.Latitude(), nil
	case "longitude":
		return gofakeit.Longitude(), nil
	case "bool":
		return gofakeit.Bool(), nil
	case "float64":
		return gofakeit.Float64(), nil
	case "word":
		return gofakeit.Word(), nil
	case "sentence":
		return gofakeit.Sentence(5), nil
	case "date":
		return gofakeit.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now()).Format("2006-01-02"), nil
	case "time":
		return gofakeit.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now()).Format("15:04:05"), nil
	case "datetime":
		return gofakeit.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now()), nil
	case "json":
		return fmt.Sprintf(`{"key":"%s","value":"%s"}`, gofakeit.Word(), gofakeit.Word()), nil
	case "":
		return nil, nil
	default:
		// Unknown faker: return a word as safe fallback
		return gofakeit.Word(), nil
	}
}

func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}
