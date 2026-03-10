package db

import (
	"reflect"
	"testing"
)

func TestParsePostgresCheckValues(t *testing.T) {
	tests := []struct {
		name   string
		clause string
		want   []string
	}{
		{
			name:   "IN normalized to ANY ARRAY with type casts",
			clause: "CHECK (((role)::text = ANY (ARRAY['admin'::text, 'user'::text, 'guest'::text])))",
			want:   []string{"admin", "user", "guest"},
		},
		{
			name:   "ANY ARRAY with character varying casts",
			clause: "CHECK ((status = ANY (ARRAY['pending'::character varying, 'active'::character varying])))",
			want:   []string{"pending", "active"},
		},
		{
			name:   "no IN or ARRAY pattern",
			clause: "CHECK ((price > 0))",
			want:   nil,
		},
		{
			name:   "single value",
			clause: "CHECK (((kind)::text = ANY (ARRAY['fixed'::text])))",
			want:   []string{"fixed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePostgresCheckValues(tt.clause)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePostgresCheckValues(%q) = %v, want %v", tt.clause, got, tt.want)
			}
		})
	}
}

func TestParsePostgresCheckRange(t *testing.T) {
	tests := []struct {
		name    string
		clause  string
		wantMin int64
		wantMax int64
		wantOK  bool
	}{
		{
			name:    "standard >= AND <= pattern",
			clause:  "CHECK (((spice_level >= 0) AND (spice_level <= 5)))",
			wantMin: 0, wantMax: 5, wantOK: true,
		},
		{
			name:    "negative lower bound",
			clause:  "CHECK ((score >= -10) AND (score <= 10))",
			wantMin: -10, wantMax: 10, wantOK: true,
		},
		{
			name:   "no range pattern",
			clause: "CHECK ((price > 0))",
			wantOK: false,
		},
		{
			name:   "IN/ARRAY clause not a range",
			clause: "CHECK (((role)::text = ANY (ARRAY['admin'::text])))",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			min, max, ok := parsePostgresCheckRange(tt.clause)
			if ok != tt.wantOK {
				t.Errorf("ok: got %v, want %v", ok, tt.wantOK)
				return
			}
			if !ok {
				return
			}
			if min != tt.wantMin || max != tt.wantMax {
				t.Errorf("range: got [%d,%d], want [%d,%d]", min, max, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestParseMySQLCheckRange(t *testing.T) {
	tests := []struct {
		name    string
		clause  string
		wantCol string
		wantMin int64
		wantMax int64
		wantOK  bool
	}{
		{
			name:    "standard >= and <= pattern",
			clause:  "(spice_level >= 0 and spice_level <= 5)",
			wantCol: "spice_level", wantMin: 0, wantMax: 5, wantOK: true,
		},
		{
			name:    "BETWEEN pattern",
			clause:  "(score between 1 and 10)",
			wantCol: "score", wantMin: 1, wantMax: 10, wantOK: true,
		},
		{
			name:   "IN clause not a range",
			clause: "(role in ('admin','user'))",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			col, min, max, ok := parseMySQLCheckRange(tt.clause)
			if ok != tt.wantOK {
				t.Errorf("ok: got %v, want %v", ok, tt.wantOK)
				return
			}
			if !ok {
				return
			}
			if col != tt.wantCol {
				t.Errorf("col: got %q, want %q", col, tt.wantCol)
			}
			if min != tt.wantMin || max != tt.wantMax {
				t.Errorf("range: got [%d,%d], want [%d,%d]", min, max, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestParseMySQLCheckClause(t *testing.T) {
	tests := []struct {
		name       string
		clause     string
		wantCol    string
		wantValues []string
	}{
		{
			name:       "plain quotes",
			clause:     "(`role` in ('admin','user','guest'))",
			wantCol:    "role",
			wantValues: []string{"admin", "user", "guest"},
		},
		{
			name:       "charset prefix _utf8mb4",
			clause:     "(role in (_utf8mb4'admin',_utf8mb4'user'))",
			wantCol:    "role",
			wantValues: []string{"admin", "user"},
		},
		{
			name:       "no backticks no prefix",
			clause:     "(status in ('pending','active','closed'))",
			wantCol:    "status",
			wantValues: []string{"pending", "active", "closed"},
		},
		{
			name:       "non-IN clause",
			clause:     "(price > 0)",
			wantCol:    "",
			wantValues: nil,
		},
		{
			name:       "uppercase IN",
			clause:     "(role IN ('admin','user'))",
			wantCol:    "role",
			wantValues: []string{"admin", "user"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCol, gotVals := parseMySQLCheckClause(tt.clause)
			if gotCol != tt.wantCol {
				t.Errorf("column: got %q, want %q", gotCol, tt.wantCol)
			}
			if !reflect.DeepEqual(gotVals, tt.wantValues) {
				t.Errorf("values: got %v, want %v", gotVals, tt.wantValues)
			}
		})
	}
}
