package faker

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
)

// MapColumnToFaker returns the most appropriate gofakeit mapping for a column.
// Priority: enum type > PK > CHECK values > CHECK range > UNIQUE > semantic name > DB type.
func MapColumnToFaker(dbType string, col db.Column) string {
	// 1. Type-level enums (e.g. PostgreSQL ENUM type, MySQL ENUM/SET)
	if len(col.EnumValues) > 0 {
		return fmt.Sprintf("randomstring(%s)", strings.Join(col.EnumValues, ","))
	}

	// 2. PK with auto-increment: no faker needed
	if col.IsPK {
		return ""
	}

	// 3. CHECK IN constraint: generate only values the constraint allows
	if len(col.CheckValues) > 0 {
		return fmt.Sprintf("randomstring(%s)", strings.Join(col.CheckValues, ","))
	}

	// 4. CHECK range constraint (col >= N AND col <= M)
	if col.CheckMin != nil && col.CheckMax != nil {
		return fmt.Sprintf("number(%d,%d)", *col.CheckMin, *col.CheckMax)
	}

	// 5. UNIQUE constraint: guarantee distinct values. String columns use uuid;
	//    numeric/temporal columns use a monotonic sequence (a uuid string can't
	//    fit them, and a random number()/date() collides at high row counts since
	//    non-PK UNIQUE columns have no retry loop). Other types fall through.
	if col.Unique {
		if uniqueUUIDSafe(col.Type) {
			return "uuid"
		}
		if uniqueSequenceForType(col.Type) {
			return uniqueSequenceFaker
		}
	}

	// 6. Strict scalar DB types should not be overridden by semantic names.
	if m := strictTypeMapper(dbType, col.Type); m != "" {
		return m
	}

	// 7. Fixed-point columns must be sized to their declared precision/scale so
	//    generated values never overflow: a scale-0 column needs an integer (not
	//    a price() float), and a small-precision column like decimal(3,2) can't
	//    hold price(1,1000) ("Out of range value"). Takes priority over semantic
	//    names (e.g. an "amount" stored as decimal(3,2)).
	if m := boundedNumericFaker(col.Type, col.DDLType); m != "" {
		return m
	}

	// 8. Semantic mapping based on column name
	if m := semanticMapper(col.Name); m != "" {
		return m
	}

	// 9. Fall back to type-based mapping
	return typeMapper(dbType, col.Type)
}

// uniqueUUIDSafe reports whether a uuid string is a valid value for a column of
// the given type. uuid is only safe for string-like and uuid columns.
func uniqueUUIDSafe(colType string) bool {
	return isStringColType(colType) || strings.ToLower(strings.TrimSpace(colType)) == "uuid"
}

// uniqueSequenceForType reports whether a UNIQUE column of this type should be
// filled with a monotonic sequence — i.e. it is numeric or temporal, where a
// uuid string can't be stored and random values collide at scale.
func uniqueSequenceForType(colType string) bool {
	t := strings.ToLower(strings.TrimSpace(colType))
	return isNumericType(t) || isTemporalType(t)
}

// isNumericType reports whether the (lowercased) DB type is a numeric type that
// can hold an integer sequence. Matches are exact to avoid false positives like
// "interval" or "point" that merely contain "int".
func isNumericType(t string) bool {
	switch t {
	case "int", "integer", "bigint", "smallint", "mediumint", "tinyint",
		"int2", "int4", "int8",
		"decimal", "numeric", "dec", "fixed",
		"real", "double", "double precision", "float", "float4", "float8",
		"money", "smallmoney",
		"serial", "bigserial", "smallserial":
		return true
	}
	return false
}

// isTemporalType reports whether the (lowercased) DB type is a date/time type.
func isTemporalType(t string) bool {
	return temporalPKFaker(t) != ""
}

var reNumericScale = regexp.MustCompile(`^(?:decimal|numeric|dec|fixed)\((\d+),\s*(\d+)\)$`)

// Default upper bounds for fixed-point generators when the column's integer-part
// capacity is larger. These preserve the historical generation ranges for wide
// columns (e.g. decimal(10,2) → price(1,1000), decimal(20,0) → number(1,100000)).
const (
	maxDecimalIntBound = 1000   // price() upper bound for fractional columns
	maxIntegerBound    = 100000 // number() upper bound for scale-0 columns
)

// boundedNumericFaker returns a generator for a fixed-point column sized to its
// declared precision/scale so generated values never exceed what the column can
// store. Returns "" when the column is not a parameterised fixed-point type
// (e.g. an unconstrained postgres `numeric`), leaving the default mapping in
// place. Engines round excess fractional digits silently, so only the integer
// magnitude needs bounding.
func boundedNumericFaker(colType, ddlType string) string {
	t := strings.ToLower(strings.TrimSpace(colType))
	if t != "decimal" && t != "numeric" {
		return ""
	}
	m := reNumericScale.FindStringSubmatch(strings.ToLower(strings.TrimSpace(ddlType)))
	if m == nil {
		return ""
	}
	precision, err1 := strconv.Atoi(m[1])
	scale, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || precision <= 0 || scale < 0 || scale > precision {
		return ""
	}

	// Largest integer part the column can hold: 10^(precision-scale) - 1.
	maxInt := pow10Minus1(precision - scale)

	// Scale 0: integers only — a fractional value would be rejected/truncated.
	if scale == 0 {
		if maxInt > maxIntegerBound {
			maxInt = maxIntegerBound
		}
		if maxInt < 1 {
			maxInt = 1
		}
		return fmt.Sprintf("number(1,%d)", maxInt)
	}

	// Scale >= 1: fractional values are fine. Bound the magnitude to the
	// integer-part capacity. When there are no integer digits (e.g.
	// numeric(2,2) → max 0.99), emit a sub-unit fraction.
	if maxInt < 1 {
		return "price(0,0.99)"
	}
	if maxInt > maxDecimalIntBound {
		maxInt = maxDecimalIntBound
	}
	return fmt.Sprintf("price(1,%d)", maxInt)
}

// pow10Minus1 returns 10^n - 1, saturating at 1e9 to avoid int overflow for
// very wide columns (the caller caps the result far below this anyway).
func pow10Minus1(n int) int {
	v := 1
	for i := 0; i < n; i++ {
		v *= 10
		if v >= 1_000_000_000 {
			return 1_000_000_000
		}
	}
	return v - 1
}

func strictTypeMapper(dbType, colType string) string {
	t := strings.ToLower(colType)
	switch dbType {
	case "mysql":
		switch t {
		case "bit", "tinyint", "bool", "boolean", "date", "time", "datetime", "timestamp", "json":
			return mysqlTypeMapper(t)
		}
	case "pgx":
		switch t {
		case "boolean", "bool", "date", "time", "time without time zone", "time with time zone",
			"timestamp", "timestamp without time zone", "timestamp with time zone", "timestamptz",
			"json", "jsonb", "uuid":
			return postgresTypeMapper(t)
		}
	}
	return ""
}

func semanticMapper(name string) string {
	n := strings.ToLower(name)
	switch {
	case n == "email" || strings.HasSuffix(n, "_email"):
		return "email"
	case n == "full_name" || n == "person_name":
		return "name"
	case n == "first_name" || n == "firstname":
		return "firstname"
	case n == "last_name" || n == "lastname":
		return "lastname"
	case n == "username" || n == "user_name":
		return "username"
	case n == "phone" || n == "phone_number" || n == "mobile":
		return "phone"
	case n == "address" || n == "street_address":
		return "street"
	case n == "city":
		return "city"
	case n == "country":
		return "country"
	case n == "zip" || n == "zip_code" || n == "postal_code":
		return "zip"
	case n == "state" || n == "province":
		return "state"
	case n == "url" || n == "website" || strings.HasSuffix(n, "_url"):
		return "url"
	case n == "date" || strings.HasSuffix(n, "_date") || n == "day" || strings.HasSuffix(n, "_day"):
		return "date"
	case n == "time" || strings.HasSuffix(n, "_time"):
		return "time"
	case n == "datetime" || n == "timestamp" || n == "created_at" || n == "updated_at" || strings.HasSuffix(n, "_at"):
		return "datetime"
	case n == "title" || strings.HasSuffix(n, "_title"):
		return "sentence"
	case n == "description" || n == "bio" || n == "summary" || n == "notes":
		return "paragraph(2)"
	case n == "price" || n == "amount" || n == "cost" || n == "total" || n == "balance":
		return "price(1,1000)"
	case strings.Contains(n, "uuid") || strings.HasSuffix(n, "_id") && strings.Contains(n, "uuid"):
		return "uuid"
	case n == "rating" || n == "score" || n == "stars":
		return "number(1,5)"
	case n == "age":
		return "number(18,90)"
	case n == "quantity" || n == "qty" || n == "stock" || n == "count":
		return "number(1,500)"
	case n == "percentage" || n == "percent" || n == "discount":
		return "number(0,100)"
	case n == "ip" || n == "ip_address":
		return "ipv4"
	case n == "color" || n == "colour":
		return "hexcolor"
	case n == "company" || n == "company_name":
		return "company"
	case n == "job" || n == "job_title" || n == "position":
		return "jobtitle"
	case n == "latitude" || n == "lat":
		return "latitude"
	case n == "longitude" || n == "lng" || n == "lon":
		return "longitude"
	}
	return ""
}

func typeMapper(dbType, colType string) string {
	t := strings.ToLower(colType)

	switch dbType {
	case "mysql":
		return mysqlTypeMapper(t)
	case "pgx":
		return postgresTypeMapper(t)
	default:
		return postgresTypeMapper(t)
	}
}

func mysqlTypeMapper(t string) string {
	switch {
	case t == "bit":
		return "bool"
	case t == "tinyint" || t == "bool" || t == "boolean":
		return "bool"
	case t == "tinyint" && strings.Contains(t, "(1)"):
		return "bool"
	case t == "smallint" || t == "mediumint" || t == "int" || t == "integer":
		return "number(1,1000)"
	case t == "bigint":
		return "number(1,100000)"
	case t == "float" || t == "double" || t == "real":
		return "float64"
	case t == "decimal" || t == "numeric":
		return "price(1,1000)"
	case t == "char" || t == "varchar":
		return "word"
	case t == "tinytext" || t == "text" || t == "mediumtext" || t == "longtext":
		return "sentence"
	case t == "date":
		return "date"
	case t == "time":
		return "time"
	case t == "datetime" || t == "timestamp":
		return "datetime"
	case t == "year":
		return "number(2000,2025)"
	case t == "json":
		return `json`
	case t == "binary" || t == "varbinary" || t == "blob" || t == "tinyblob" || t == "mediumblob" || t == "longblob":
		return "word"
	default:
		return "word"
	}
}

func postgresTypeMapper(t string) string {
	switch {
	case t == "boolean" || t == "bool":
		return "bool"
	case t == "smallint" || t == "int2":
		return "number(1,1000)"
	case t == "integer" || t == "int" || t == "int4":
		return "number(1,10000)"
	case t == "bigint" || t == "int8":
		return "number(1,100000)"
	case t == "serial" || t == "bigserial" || t == "smallserial":
		return ""
	case t == "real" || t == "float4" || t == "float8" || t == "double precision":
		return "float64"
	case t == "numeric" || t == "decimal":
		return "price(1,1000)"
	case t == "money":
		return "price(1,10000)"
	case t == "char" || t == "character" || t == "bpchar":
		return "word"
	case t == "varchar" || t == "character varying":
		return "word"
	case t == "text":
		return "sentence"
	case t == "uuid":
		return "uuid"
	case t == "date":
		return "date"
	case t == "time" || t == "time without time zone" || t == "time with time zone":
		return "time"
	case t == "timestamp" || t == "timestamp without time zone" || t == "timestamp with time zone" || t == "timestamptz":
		return "datetime"
	case t == "interval":
		return "word"
	case t == "json" || t == "jsonb":
		return "json"
	case t == "inet" || t == "cidr":
		return "ipv4"
	case t == "macaddr":
		return "macaddress"
	case t == "bytea":
		return "word"
	case t == "xml":
		return "word"
	default:
		return "word"
	}
}
