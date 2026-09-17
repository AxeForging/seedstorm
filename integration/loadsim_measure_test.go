//go:build integration && loadsim

package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// measurement is one seed run on a limited database.
type measurement struct {
	Profile    string  `json:"profile"`
	Engine     string  `json:"engine"`
	Writers    int     `json:"writers"`
	Rows       int     `json:"rows"`
	Seconds    float64 `json:"seconds"`
	RowsPerSec float64 `json:"rowsPerSec"`
	OOMKilled  bool    `json:"oomKilled"`
}

// TestLoadsim_MeasureWriters measures seed throughput per profile and writer
// count, to calibrate the tuning rules. It asserts nothing and runs only when
// SEEDSTORM_LOADSIM_MEASURE is set (the output path for a JSON report);
// timings on shared CI runners are for trends, not gates.
func TestLoadsim_MeasureWriters(t *testing.T) {
	out := os.Getenv("SEEDSTORM_LOADSIM_MEASURE")
	if out == "" {
		t.Skip("set SEEDSTORM_LOADSIM_MEASURE=<report.json> to measure")
	}
	profiles := strings.Split(envOrDefault("SEEDSTORM_LOADSIM_PROFILES", "cloudsql-micro"), ",")
	engines := strings.Split(envOrDefault("SEEDSTORM_LOADSIM_ENGINES", "pgx,mysql"), ",")
	rows := 2000
	var results []measurement
	for _, name := range profiles {
		p, ok := loadsimProfiles[name]
		if !ok {
			t.Fatalf("unknown profile %q", name)
		}
		for _, driver := range engines {
			requireHeadroom(t, 3072)
			d := startLoadsimDB(t, driver, p, true)
			schema := `
				CREATE TABLE customers (id INT PRIMARY KEY, email VARCHAR(120) NOT NULL, name VARCHAR(80), created_at TIMESTAMP);
				CREATE TABLE orders (id INT PRIMARY KEY, customer_id INT NOT NULL, total DECIMAL(10,2), note VARCHAR(200),
					FOREIGN KEY (customer_id) REFERENCES customers (id));
				CREATE TABLE items (id INT PRIMARY KEY, order_id INT NOT NULL, sku VARCHAR(40), qty INT,
					FOREIGN KEY (order_id) REFERENCES orders (id))`
			execSQL(t, d.conn, schema)
			schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
			engineName := map[string]string{postgresDriver: "postgres", mysqlDriver: "mysql"}[driver]
			runBin(t, "introspect", "--db", engineName, "--dsn", d.dsn, "--out", schemaPath)
			for _, writers := range []int{1, 2, 4, 8} {
				start := time.Now()
				runBin(t, "seed", "--db", engineName, "--dsn", d.dsn, "--schema", schemaPath,
					"--rows", fmt.Sprint(rows), "--truncate", "--yes", "--workers", fmt.Sprint(writers))
				secs := time.Since(start).Seconds()
				m := measurement{
					Profile: name, Engine: engineName, Writers: writers, Rows: rows * 3, Seconds: secs,
					RowsPerSec: float64(rows*3) / secs, OOMKilled: d.oomKilled(t),
				}
				t.Logf("%s %-8s writers=%d  %.1fs  %.0f rows/s  oom=%v", m.Profile, m.Engine, m.Writers, m.Seconds, m.RowsPerSec, m.OOMKilled)
				results = append(results, m)
			}
		}
	}
	raw, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
