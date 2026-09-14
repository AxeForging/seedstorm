//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Keycloak fixtures are a real application's schema (87 tables, varchar(36)
// ids, 46 composite primary keys, upper-case MySQL identifiers). They prove the
// seed → clone → compare → mirror workflow on a schema nobody tailored for
// seedstorm.

// keycloakProfile uses every action kind, written with Postgres (lower-case)
// names so it must also drive MySQL's upper-case USER_ENTITY. realm_id is
// narrowed to two values inside UNIQUE (realm_id, username) and
// (realm_id, email_constraint), the constraints Keycloak really has.
const keycloakProfile = `name: keycloak-load
rules:
  - column: "email"
    template: "kc+{{seq}}.{{run}}@load.test"
  - column: "*_name"
    template: "LT {{auto}}"
tables:
  user_entity:
    rows: 30
    columns:
      username: { template: "kc_user_{{run}}_{{seq}}" }
      enabled: { value: true }
      created_timestamp: { faker: "number(1700000000000,1700000009999)" }
      federation_link: { setNull: true }
      realm_id: { oneOf: [realm-a, realm-b] }
  client:
    columns:
      description: { template: "{{table}}.{{column}} run {{run}}" }
`

// keycloakProfileViolations counts user rows that ignore any rule of keycloakProfile.
func keycloakProfileViolations(t *testing.T, e engine, conn *sql.DB) (violations, total int) {
	t.Helper()
	table := "user_entity"
	if e.driver == mysqlDriver {
		table = "USER_ENTITY"
	}
	enabled := "enabled"
	if e.driver == mysqlDriver {
		enabled = "enabled = 1"
	}
	total = scalar(t, conn, "SELECT COUNT(*) FROM "+table)
	violations = scalar(t, conn, `SELECT COUNT(*) FROM `+table+` WHERE
		email NOT LIKE 'kc+%@load.test' OR username NOT LIKE 'kc\_user\_%' OR first_name NOT LIKE 'LT %'
		OR NOT (`+enabled+`) OR federation_link IS NOT NULL OR realm_id NOT IN ('realm-a', 'realm-b')
		OR created_timestamp NOT BETWEEN 1700000000000 AND 1700000009999`)
	return violations, total
}

func loadKeycloak(t *testing.T, e engine, conn *sql.DB) {
	t.Helper()
	if e.driver == postgresDriver {
		execScript(t, conn, "fixtures/keycloak_postgres.sql")
		return
	}
	raw, err := os.ReadFile("fixtures/keycloak_mysql.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The dump creates tables before the ones they reference: FK checks must be
	// off, and SET only applies to one session, so pin a single connection.
	ctx := context.Background()
	c, err := conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		t.Fatal(err)
	}
	ddl := string(raw)
	if !mysqlHasCheckSupport(t, conn) {
		// The dump comes from MySQL 8; 5.7 lacks the 0900 collations and the utf8mb3 alias.
		ddl = strings.NewReplacer("utf8mb4_0900_ai_ci", "utf8mb4_unicode_ci", "utf8mb3", "utf8").Replace(ddl)
	}
	var body strings.Builder
	for _, line := range strings.Split(ddl, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			body.WriteString(line + "\n")
		}
	}
	for _, stmt := range strings.Split(body.String(), ";") {
		if stmt = strings.TrimSpace(stmt); stmt == "" {
			continue
		}
		if _, err := c.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("keycloak DDL [%.60s]: %v", stmt, err)
		}
	}
	if _, err := c.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1"); err != nil {
		t.Fatal(err)
	}
}

func TestKeycloak_SeedCloneAndMirror(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "keycloak.yaml")
	if err := os.WriteFile(profilePath, []byte(keycloakProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			srcDSN, src := e.scratchDB(t, "ss_kc_src")
			tgtDSN, tgt := e.scratchDB(t, "ss_kc_tgt")
			loadKeycloak(t, e, src)
			if tables := tableCounts(t, e, src); len(tables) != 87 {
				t.Fatalf("fixture loaded %d tables, want 87", len(tables))
			}
			schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", srcDSN, "--out", schemaPath)
			runBin(t, "seed", "--db", e.name, "--dsn", srcDSN, "--schema", schemaPath, "--rows", "8", "--profile", profilePath)
			source := tableCounts(t, e, src)
			for table, n := range source {
				if n == 0 {
					t.Errorf("seed left %s empty", table)
				}
			}
			if bad, total := keycloakProfileViolations(t, e, src); bad != 0 || total != 30 {
				t.Errorf("source users: %d of %d ignore the profile, want 0 of 30", bad, total)
			}

			t.Run("generate applies the profile without a database", func(t *testing.T) {
				out := runBin(t, "generate", "--schema", schemaPath, "--db", e.name, "--rows", "4", "--format", "json", "--profile", profilePath)
				data := decodeJSON[map[string][]map[string]any](t, out)
				var users []map[string]any
				for table, rows := range data {
					if strings.EqualFold(table, "user_entity") {
						users = rows
					}
				}
				if len(users) != 30 {
					t.Fatalf("generated %d users, want the profile's 30", len(users))
				}
				pairs := map[string]bool{}
				for _, raw := range users {
					u := map[string]any{}
					for k, v := range raw {
						u[strings.ToLower(k)] = v
					}
					if !strings.HasPrefix(u["username"].(string), "kc_user_") || u["federation_link"] != nil ||
						(u["realm_id"] != "realm-a" && u["realm_id"] != "realm-b") || !strings.HasSuffix(u["email"].(string), "@load.test") {
						t.Fatalf("generated user ignores the profile: %v", u)
					}
					pair := u["realm_id"].(string) + "|" + u["username"].(string)
					if pairs[pair] {
						t.Fatalf("UNIQUE (realm_id, username) repeated: %s", pair)
					}
					pairs[pair] = true
				}
			})

			runBin(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", tgtDSN)
			endpoints := []string{"--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", tgtDSN}

			runBin(t, append([]string{"mirror", "--profile", profilePath}, endpoints...)...)
			assertCounts(t, tableCounts(t, e, tgt), source, 1)

			runBin(t, append([]string{"mirror", "--scale", "2", "--profile", profilePath}, endpoints...)...)
			assertCounts(t, tableCounts(t, e, tgt), source, 2)

			if bad, total := keycloakProfileViolations(t, e, tgt); bad != 0 || total != 60 {
				t.Errorf("target users: %d of %d ignore the profile, want 0 of 60", bad, total)
			}
			assertCounts(t, tableCounts(t, e, src), source, 1)
		})
	}

	t.Run("mysql source to postgres target", func(t *testing.T) {
		my, pg := mysqlEngine(), postgresEngine()
		srcDSN, src := my.scratchDB(t, "ss_kc_xsrc")
		tgtDSN, tgt := pg.scratchDB(t, "ss_kc_xtgt")
		loadKeycloak(t, my, src)
		loadKeycloak(t, pg, tgt)
		schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
		runBin(t, "introspect", "--db", "mysql", "--dsn", srcDSN, "--out", schemaPath)
		runBin(t, "seed", "--db", "mysql", "--dsn", srcDSN, "--schema", schemaPath, "--rows", "6")

		runBin(t, "mirror", "--source-db", "mysql", "--source-dsn", srcDSN, "--target-db", "postgres", "--target-dsn", tgtDSN, "--mode", "reset", "--scale", "0.5", "--yes")
		source := map[string]int{}
		for table, n := range tableCounts(t, my, src) {
			source[strings.ToLower(table)] = n
		}
		assertCounts(t, tableCounts(t, pg, tgt), source, 0.5)
	})
}
