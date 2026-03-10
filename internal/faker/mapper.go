package faker

import (
	"fmt"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
)

// MapColumnToFaker returns the most appropriate gofakeit mapping for a column.
// It first checks for semantic hints in the column name, then falls back to DB type.
func MapColumnToFaker(dbType string, col db.Column) string {
	// Enum/set: pick from provided values
	if len(col.EnumValues) > 0 {
		return fmt.Sprintf("randomstring(%s)", strings.Join(col.EnumValues, ","))
	}

	// PK with auto-increment: no faker needed
	if col.IsPK {
		return ""
	}

	// Semantic mapping based on column name
	if m := semanticMapper(col.Name); m != "" {
		return m
	}

	// Fall back to type-based mapping
	return typeMapper(dbType, col.Type)
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
