package faker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// Generator describes one generator the engine understands, for pickers and
// template help. Expr is the canonical expression, including default
// arguments for parameterised generators.
type Generator struct {
	Name        string   `json:"name"`
	Expr        string   `json:"expr"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Params      []string `json:"params,omitempty"`
}

// catalog lists every generator generate() implements, grouped for display.
// Keep in sync with knownFakers / knownParamFakers (see TestCatalog_MatchesEngine).
var catalog = []Generator{
	{Name: "name", Expr: "name", Category: "Person", Description: "Full name"},
	{Name: "firstname", Expr: "firstname", Category: "Person", Description: "First name"},
	{Name: "lastname", Expr: "lastname", Category: "Person", Description: "Last name"},
	{Name: "username", Expr: "username", Category: "Person", Description: "Login handle"},
	{Name: "jobtitle", Expr: "jobtitle", Category: "Person", Description: "Job title"},
	{Name: "email", Expr: "email", Category: "Internet", Description: "Email address"},
	{Name: "url", Expr: "url", Category: "Internet", Description: "Web URL"},
	{Name: "domain", Expr: "domain", Category: "Internet", Description: "Domain name"},
	{Name: "ipv4", Expr: "ipv4", Category: "Internet", Description: "IPv4 address"},
	{Name: "macaddress", Expr: "macaddress", Category: "Internet", Description: "MAC address"},
	{Name: "phone", Expr: "phone", Category: "Location", Description: "Phone number"},
	{Name: "street", Expr: "street", Category: "Location", Description: "Street address"},
	{Name: "city", Expr: "city", Category: "Location", Description: "City"},
	{Name: "state", Expr: "state", Category: "Location", Description: "State or province"},
	{Name: "country", Expr: "country", Category: "Location", Description: "Country"},
	{Name: "zip", Expr: "zip", Category: "Location", Description: "Postal code"},
	{Name: "latitude", Expr: "latitude", Category: "Location", Description: "Latitude"},
	{Name: "longitude", Expr: "longitude", Category: "Location", Description: "Longitude"},
	{Name: "company", Expr: "company", Category: "Business", Description: "Company name"},
	{Name: "productname", Expr: "productname", Category: "Business", Description: "Product name"},
	{Name: "price", Expr: "price(1,1000)", Category: "Business", Description: "Decimal price between min and max", Params: []string{"min", "max"}},
	{Name: "word", Expr: "word", Category: "Text", Description: "Single word"},
	{Name: "sentence", Expr: "sentence", Category: "Text", Description: "Short sentence"},
	{Name: "paragraph", Expr: "paragraph(1)", Category: "Text", Description: "Paragraphs of text", Params: []string{"count"}},
	{Name: "lexify", Expr: "lexify(????)", Category: "Text", Description: "Replace each ? with a random letter", Params: []string{"pattern"}},
	{Name: "numerify", Expr: "numerify(###-####)", Category: "Text", Description: "Replace each # with a random digit", Params: []string{"pattern"}},
	{Name: "hexcolor", Expr: "hexcolor", Category: "Text", Description: "Hex color like #a1b2c3"},
	{Name: "number", Expr: "number(1,100)", Category: "Numbers", Description: "Integer between min and max", Params: []string{"min", "max"}},
	{Name: "float64", Expr: "float64", Category: "Numbers", Description: "Random float"},
	{Name: "bool", Expr: "bool", Category: "Numbers", Description: "true or false"},
	{Name: "randomstring", Expr: "randomstring(a,b,c)", Category: "Numbers", Description: "One of the listed values", Params: []string{"values..."}},
	{Name: "date", Expr: "date", Category: "Dates", Description: "Date (YYYY-MM-DD)"},
	{Name: "time", Expr: "time", Category: "Dates", Description: "Time of day (HH:MM:SS)"},
	{Name: "datetime", Expr: "datetime", Category: "Dates", Description: "Timestamp"},
	{Name: "daterange", Expr: "daterange(2025-01-01,2026-01-01)", Category: "Dates", Description: "Date from (inclusive) to (exclusive)", Params: []string{"from", "to"}},
	{Name: "datetimerange", Expr: "datetimerange(2025-01-01 00:00:00,2026-01-01 00:00:00)", Category: "Dates", Description: "Timestamp from (inclusive) to (exclusive)", Params: []string{"from", "to"}},
	{Name: "uuid", Expr: "uuid", Category: "Identifiers", Description: "Random UUID"},
	{Name: "json", Expr: "json", Category: "Identifiers", Description: "Small JSON object"},
}

// Catalog returns every generator, sorted by category then name.
func Catalog() []Generator {
	out := make([]Generator, len(catalog))
	copy(out, catalog)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Evaluate runs one generator expression, e.g. "email" or "number(1,9)".
// Unlike the schema path, an unknown expression is an error, so rule authors
// see typos instead of silent random words.
func Evaluate(expr string) (interface{}, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" || expr == uniqueSequenceFaker || !ValidFaker(expr) {
		return nil, fmt.Errorf("unknown generator %q", expr)
	}
	return generate(expr)
}

// BuildSchema converts introspected tables into a schema with default faker
// mappings — the same shape `seedstorm introspect` writes.
func BuildSchema(dbType string, tables []db.Table) *schema.Schema {
	out := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
	for _, t := range tables {
		st := schema.Table{Columns: make(map[string]schema.Column, len(t.Columns))}
		for _, c := range t.Columns {
			sc := schema.Column{
				Type:      c.Type,
				DDLType:   c.DDLType,
				PK:        c.IsPK,
				Nullable:  c.IsNullable,
				Unique:    c.Unique,
				Generated: c.Generated != "",
				Faker:     MapColumnToFaker(dbType, c),
			}
			if c.FK != nil {
				sc.FK = fmt.Sprintf("%s.%s", c.FK.TableName, c.FK.ColumnName)
			}
			st.Columns[c.Name] = sc
		}
		for _, idx := range t.Indexes {
			if idx.Unique && len(idx.Columns) > 1 {
				st.Unique = append(st.Unique, append([]string(nil), idx.Columns...))
			}
		}
		if p := t.Partition; p != nil {
			applyPartitioning(&st, t, p)
		}
		out.Tables[t.Name] = st
	}
	return out
}
