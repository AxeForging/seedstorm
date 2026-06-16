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

func TestMapColumnToFaker_ZeroScaleDecimalGetsInteger(t *testing.T) {
	// A fixed-point column with scale 0 can never store a fractional value, so
	// it must map to an integer generator rather than price() (which yields
	// floats and triggers "Data truncated" on insert).
	tests := []struct {
		name   string
		driver string
		col    db.Column
		want   string
	}{
		{
			name:   "mysql decimal(20,0)",
			driver: "mysql",
			col:    db.Column{Name: "external_order_id", Type: "decimal", DDLType: "decimal(20,0)"},
			want:   "number(1,100000)",
		},
		{
			name:   "postgres numeric(20,0)",
			driver: "pgx",
			col:    db.Column{Name: "external_order_id", Type: "numeric", DDLType: "numeric(20,0)"},
			want:   "number(1,100000)",
		},
		{
			name:   "small precision caps to precision max",
			driver: "mysql",
			col:    db.Column{Name: "code_num", Type: "decimal", DDLType: "decimal(4,0)"},
			want:   "number(1,9999)",
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

func TestMapColumnToFaker_NonZeroScaleDecimalStillPrice(t *testing.T) {
	// Wide decimals with room for the default 1000 upper bound keep price(1,1000).
	col := db.Column{Name: "amount", Type: "decimal", DDLType: "decimal(10,2)"}
	if got := MapColumnToFaker("mysql", col); got != "price(1,1000)" {
		t.Errorf("decimal(10,2) got %q, want price(1,1000)", got)
	}
}

func TestMapColumnToFaker_SmallPrecisionDecimalBoundedToPrecision(t *testing.T) {
	// A fractional decimal whose precision can't hold price(1,1000) must be
	// bounded to its integer-part capacity, or the insert fails with
	// "Out of range value" (MySQL 1264 / Postgres numeric overflow).
	tests := []struct {
		name   string
		driver string
		col    db.Column
		want   string
	}{
		{"mysql decimal(3,2) bounded fraction", "mysql", db.Column{Name: "usage_ratio", Type: "decimal", DDLType: "decimal(3,2)"}, "price(1,9)"},
		{"pgx numeric(3,2)", "pgx", db.Column{Name: "ratio", Type: "numeric", DDLType: "numeric(3,2)"}, "price(1,9)"},
		{"mysql decimal(5,2)", "mysql", db.Column{Name: "pct", Type: "decimal", DDLType: "decimal(5,2)"}, "price(1,999)"},
		{"mysql decimal(2,2) sub-unit only", "mysql", db.Column{Name: "frac", Type: "decimal", DDLType: "decimal(2,2)"}, "price(0,0.99)"},
		{"pgx numeric(4,1)", "pgx", db.Column{Name: "tenths", Type: "numeric", DDLType: "numeric(4,1)"}, "price(1,999)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapColumnToFaker(tt.driver, tt.col); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapColumnToFaker_UniqueNumericGetsSequence(t *testing.T) {
	// A UNIQUE numeric column must NOT receive a uuid string (that triggers
	// "Data truncated") NOR a random number() (that collides at high row counts,
	// "Duplicate entry"). It gets a guaranteed-unique monotonic sequence.
	tests := []struct {
		name   string
		driver string
		col    db.Column
	}{
		{"mysql unique bigint", "mysql", db.Column{Name: "external_order_id", Type: "bigint", DDLType: "bigint", Unique: true}},
		{"postgres unique bigint", "pgx", db.Column{Name: "external_order_id", Type: "bigint", DDLType: "bigint", Unique: true}},
		{"mysql unique int", "mysql", db.Column{Name: "ext_id", Type: "int", DDLType: "int", Unique: true}},
		{"postgres unique numeric(20,0)", "pgx", db.Column{Name: "ext_id", Type: "numeric", DDLType: "numeric(20,0)", Unique: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapColumnToFaker(tt.driver, tt.col); got != "sequence" {
				t.Errorf("got %q, want %q", got, "sequence")
			}
		})
	}
}

func TestMapColumnToFaker_UniqueStringStillUUID(t *testing.T) {
	// String UNIQUE columns keep using uuid for collision-free values.
	for _, typ := range []string{"varchar", "character varying", "char", "text"} {
		col := db.Column{Name: "slug", Type: typ, Unique: true}
		if got := MapColumnToFaker("pgx", col); got != "uuid" {
			t.Errorf("unique %s got %q, want uuid", typ, got)
		}
	}
}

func TestMapColumnToFaker_UniqueTemporalGetsSequence(t *testing.T) {
	// A UNIQUE temporal column gets a monotonic sequence too — a uuid string
	// can't fit it, and random dates/timestamps collide at high row counts.
	tests := []struct {
		name   string
		driver string
		col    db.Column
	}{
		{"pgx unique date", "pgx", db.Column{Name: "event_date", Type: "date", DDLType: "date", Unique: true}},
		{"pgx unique timestamp", "pgx", db.Column{Name: "fired_at", Type: "timestamp without time zone", DDLType: "timestamp without time zone", Unique: true}},
		{"mysql unique datetime", "mysql", db.Column{Name: "fired_at", Type: "datetime", DDLType: "datetime", Unique: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapColumnToFaker(tt.driver, tt.col); got != "sequence" {
				t.Errorf("got %q, want %q", got, "sequence")
			}
		})
	}
}

func TestMapColumnToFaker_PostgresMoney(t *testing.T) {
	// A money column whose name is NOT a price-semantic word exercises the money
	// type mapping directly. (Semantic names like "balance"/"amount" win first
	// and also yield a valid price, so use a neutral name here.)
	col := db.Column{Name: "wallet", Type: "money", DDLType: "money"}
	if got := MapColumnToFaker("pgx", col); got != "price(1,10000)" {
		t.Errorf("money got %q, want price(1,10000)", got)
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
