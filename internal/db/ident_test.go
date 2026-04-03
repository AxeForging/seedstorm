package db

import "testing"

func TestQuoteIdent_postgres(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "users", `"users"`},
		{"reserved word user", "user", `"user"`},
		{"reserved word order", "order", `"order"`},
		{"reserved word group", "group", `"group"`},
		{"reserved word check", "check", `"check"`},
		{"reserved word table", "table", `"table"`},
		{"embedded double-quote", `my"table`, `"my""table"`},
		{"multiple double-quotes", `a"b"c`, `"a""b""c"`},
		{"empty string", "", `""`},
		{"underscore name", "user_id", `"user_id"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QuoteIdent(tt.in, "pgx")
			if got != tt.want {
				t.Errorf("QuoteIdent(%q, \"pgx\") = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestQuoteIdent_mysql(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "users", "`users`"},
		{"reserved word user", "user", "`user`"},
		{"reserved word order", "order", "`order`"},
		{"reserved word group", "group", "`group`"},
		{"reserved word check", "check", "`check`"},
		{"reserved word table", "table", "`table`"},
		{"embedded backtick", "my`table", "`my``table`"},
		{"multiple backticks", "a`b`c", "`a``b``c`"},
		{"empty string", "", "``"},
		{"underscore name", "user_id", "`user_id`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QuoteIdent(tt.in, "mysql")
			if got != tt.want {
				t.Errorf("QuoteIdent(%q, \"mysql\") = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
