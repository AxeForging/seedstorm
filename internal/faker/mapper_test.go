package faker

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
)

func TestMapColumnToFaker_CheckValues(t *testing.T) {
	col := db.Column{
		Name:        "role",
		Type:        "character varying",
		CheckValues: []string{"admin", "user", "guest"},
	}
	got := MapColumnToFaker("pgx", col)
	want := "randomstring(admin,user,guest)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMapColumnToFaker_CheckValuesTakesPriorityOverSemantic(t *testing.T) {
	// A column named "status" with CHECK values should use randomstring, not word
	col := db.Column{
		Name:        "status",
		Type:        "character varying",
		CheckValues: []string{"pending", "active", "closed"},
	}
	got := MapColumnToFaker("pgx", col)
	want := "randomstring(pending,active,closed)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMapColumnToFaker_UniqueGetsUUID(t *testing.T) {
	col := db.Column{
		Name:   "slug",
		Type:   "character varying",
		Unique: true,
	}
	got := MapColumnToFaker("pgx", col)
	if got != "uuid" {
		t.Errorf("got %q, want %q", got, "uuid")
	}
}

func TestMapColumnToFaker_UniqueEmailGetsUUID(t *testing.T) {
	col := db.Column{
		Name:   "email",
		Type:   "character varying",
		Unique: true,
	}
	got := MapColumnToFaker("pgx", col)
	if got != "uuid" {
		t.Errorf("UNIQUE email should get uuid to avoid collisions, got %q", got)
	}
}

func TestMapColumnToFaker_EnumTakesPriorityOverUnique(t *testing.T) {
	// Type-level enum takes priority over UNIQUE
	col := db.Column{
		Name:       "status",
		Type:       "order_status",
		EnumValues: []string{"pending", "done"},
		Unique:     true,
	}
	got := MapColumnToFaker("pgx", col)
	want := "randomstring(pending,done)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMapColumnToFaker_CheckTakesPriorityOverUnique(t *testing.T) {
	col := db.Column{
		Name:        "role",
		Type:        "character varying",
		Unique:      true,
		CheckValues: []string{"admin", "user"},
	}
	got := MapColumnToFaker("pgx", col)
	want := "randomstring(admin,user)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMapColumnToFaker_PKStillEmpty(t *testing.T) {
	col := db.Column{Name: "id", Type: "integer", IsPK: true, Unique: true}
	if got := MapColumnToFaker("pgx", col); got != "" {
		t.Errorf("PK should return empty faker, got %q", got)
	}
}

func TestMapColumnToFaker_NonUniqueFallsBackToSemantic(t *testing.T) {
	col := db.Column{Name: "email", Type: "character varying"}
	got := MapColumnToFaker("pgx", col)
	if got != "email" {
		t.Errorf("non-unique email should use semantic faker 'email', got %q", got)
	}
}

func TestMapColumnToFaker_MySQLCheckValues(t *testing.T) {
	col := db.Column{
		Name:        "discount_type",
		Type:        "varchar",
		CheckValues: []string{"percentage", "fixed"},
	}
	got := MapColumnToFaker("mysql", col)
	want := "randomstring(percentage,fixed)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMapColumnToFaker_MySQLUniqueGetsUUID(t *testing.T) {
	col := db.Column{
		Name:   "code",
		Type:   "varchar",
		Unique: true,
	}
	got := MapColumnToFaker("mysql", col)
	if got != "uuid" {
		t.Errorf("got %q, want %q", got, "uuid")
	}
}

func TestMapColumnToFaker_MySQLBitGetsBool(t *testing.T) {
	col := db.Column{
		Name:    "enabled",
		Type:    "bit",
		DDLType: "bit(1)",
	}
	got := MapColumnToFaker("mysql", col)
	if got != "bool" {
		t.Errorf("got %q, want %q", got, "bool")
	}
}

func TestMapColumnToFaker_StrictTemporalTypeWinsOverSemanticName(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		col    db.Column
		want   string
	}{
		{
			name:   "mysql date",
			driver: "mysql",
			col:    db.Column{Name: "total", Type: "date", DDLType: "date"},
			want:   "date",
		},
		{
			name:   "postgres timestamp",
			driver: "pgx",
			col:    db.Column{Name: "score", Type: "timestamp without time zone", DDLType: "timestamp without time zone"},
			want:   "datetime",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MapColumnToFaker(tt.driver, tt.col)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapColumnToFaker_SemanticTemporalNames(t *testing.T) {
	tests := []struct {
		name string
		col  db.Column
		want string
	}{
		{name: "date suffix", col: db.Column{Name: "completed_date", Type: "varchar"}, want: "date"},
		{name: "day suffix", col: db.Column{Name: "business_day", Type: "varchar"}, want: "date"},
		{name: "time suffix", col: db.Column{Name: "cutoff_time", Type: "varchar"}, want: "time"},
		{name: "at suffix", col: db.Column{Name: "processed_at", Type: "varchar"}, want: "datetime"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MapColumnToFaker("mysql", tt.col)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapColumnToFaker_CheckRangeGetsNumber(t *testing.T) {
	min, max := int64(0), int64(5)
	col := db.Column{
		Name:     "spice_level",
		Type:     "integer",
		CheckMin: &min,
		CheckMax: &max,
	}
	got := MapColumnToFaker("pgx", col)
	want := "number(0,5)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMapColumnToFaker_CheckRangeMySQL(t *testing.T) {
	min, max := int64(1), int64(10)
	col := db.Column{
		Name:     "rating",
		Type:     "smallint",
		CheckMin: &min,
		CheckMax: &max,
	}
	got := MapColumnToFaker("mysql", col)
	want := "number(1,10)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMapColumnToFaker_CheckRangeTakesPriorityOverSemantic(t *testing.T) {
	// "rating" has semantic mapping number(1,5), but explicit range should win
	min, max := int64(0), int64(100)
	col := db.Column{
		Name:     "rating",
		Type:     "integer",
		CheckMin: &min,
		CheckMax: &max,
	}
	got := MapColumnToFaker("pgx", col)
	want := "number(0,100)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
