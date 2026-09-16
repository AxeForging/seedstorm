package seeder

import (
	"context"
	"fmt"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// flatSchema is n independent tables of typical columns: the best case for
// concurrent generation, and a measure of the generator's own throughput.
func flatSchema(n int) (*schema.Schema, []string) {
	sc := &schema.Schema{Tables: map[string]schema.Table{}}
	var order []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("flat%03d", i)
		sc.Tables[name] = schema.Table{Columns: map[string]schema.Column{
			"id":         {Type: "bigint", PK: true},
			"name":       {Type: "varchar(60)", Faker: "name"},
			"email":      {Type: "varchar(80)", Faker: "email"},
			"city":       {Type: "varchar(60)", Faker: "city"},
			"amount":     {Type: "numeric", Faker: "price(1,1000)"},
			"created_at": {Type: "timestamp", Faker: "datetime"},
		}}
		order = append(order, name)
	}
	return sc, order
}

// BenchmarkSeed_GenWorkers reports rows/s generated and handed to an instant
// in-memory database, so only generation is measured.
func BenchmarkSeed_GenWorkers(b *testing.B) {
	sc, order := flatSchema(32)
	for _, gen := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("gen=%d", gen), func(b *testing.B) {
			conn, _ := openRecordingB(b)
			rows := 0
			for i := 0; i < b.N; i++ {
				res, err := Seed(context.Background(), conn, "mysql", sc, order, order, SeedOptions{
					Rows: 5000, BatchSize: 1000, Workers: 8, GenWorkers: gen,
				})
				if err != nil {
					b.Fatal(err)
				}
				rows += res.Total
			}
			b.ReportMetric(float64(rows)/b.Elapsed().Seconds(), "rows/s")
		})
	}
}
