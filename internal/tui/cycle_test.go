package tui

import "github.com/AxeForging/seedstorm/internal/schema"

func hardSelfReferenceTUISchema() *schema.Schema {
	return makeSchema(map[string]map[string]schema.Column{
		"employees": {
			"id":         {Type: "integer", PK: true},
			"manager_id": {Type: "integer", FK: "employees.id"},
		},
	})
}
