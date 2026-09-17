package faker

import (
	"sync"
	"time"

	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/brianvoe/gofakeit/v6"
)

// reproducibleEnd is the end of generated date ranges in a seeded run. A run
// ending its ranges at the current time could not reproduce its own dates.
var reproducibleEnd = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var (
	clockMu sync.RWMutex
	seeded  bool
)

// SeedRandom makes generation reproducible: every random draw comes from one
// source seeded with seed, and date ranges end at a fixed instant instead of
// now. A zero seed keeps generation random.
func SeedRandom(seed int64) {
	clockMu.Lock()
	defer clockMu.Unlock()
	seeded = seed != 0
	if seeded {
		gofakeit.Seed(seed)
	}
}

func dateRangeEnd() time.Time {
	clockMu.RLock()
	defer clockMu.RUnlock()
	if seeded {
		return reproducibleEnd
	}
	return time.Now()
}

// randomSource is where generation draws random values: gofakeit's global,
// seeded source (see SeedRandom), or a private one per concurrent generator
// (*gofakeit.Faker has the same methods), so generators never contend on the
// global source's lock.
type randomSource interface {
	Bool() bool
	City() string
	Company() string
	Country() string
	DateRange(start, end time.Time) time.Time
	DomainName() string
	Email() string
	FirstName() string
	Float64() float64
	HexColor() string
	IPv4Address() string
	JobTitle() string
	LastName() string
	Latitude() float64
	Lexify(str string) string
	Longitude() float64
	MacAddress() string
	Name() string
	Number(min, max int) int
	Numerify(str string) string
	Paragraph(paragraphCount, sentenceCount, wordCount int, separator string) string
	Phone() string
	Price(min, max float64) float64
	ProductName() string
	RandomString(a []string) string
	Sentence(wordCount int) string
	State() string
	Street() string
	URL() string
	UUID() string
	Username() string
	Word() string
	Zip() string
}

// globalSource draws from gofakeit's global source.
type globalSource struct{}

func (globalSource) Bool() bool                               { return gofakeit.Bool() }
func (globalSource) City() string                             { return gofakeit.City() }
func (globalSource) Company() string                          { return gofakeit.Company() }
func (globalSource) Country() string                          { return gofakeit.Country() }
func (globalSource) DateRange(start, end time.Time) time.Time { return gofakeit.DateRange(start, end) }
func (globalSource) DomainName() string                       { return gofakeit.DomainName() }
func (globalSource) Email() string                            { return gofakeit.Email() }
func (globalSource) FirstName() string                        { return gofakeit.FirstName() }
func (globalSource) Float64() float64                         { return gofakeit.Float64() }
func (globalSource) HexColor() string                         { return gofakeit.HexColor() }
func (globalSource) IPv4Address() string                      { return gofakeit.IPv4Address() }
func (globalSource) JobTitle() string                         { return gofakeit.JobTitle() }
func (globalSource) LastName() string                         { return gofakeit.LastName() }
func (globalSource) Latitude() float64                        { return gofakeit.Latitude() }
func (globalSource) Lexify(str string) string                 { return gofakeit.Lexify(str) }
func (globalSource) Longitude() float64                       { return gofakeit.Longitude() }
func (globalSource) MacAddress() string                       { return gofakeit.MacAddress() }
func (globalSource) Name() string                             { return gofakeit.Name() }
func (globalSource) Number(min, max int) int                  { return gofakeit.Number(min, max) }
func (globalSource) Numerify(str string) string               { return gofakeit.Numerify(str) }
func (globalSource) Paragraph(paragraphCount, sentenceCount, wordCount int, separator string) string {
	return gofakeit.Paragraph(paragraphCount, sentenceCount, wordCount, separator)
}
func (globalSource) Phone() string                  { return gofakeit.Phone() }
func (globalSource) Price(min, max float64) float64 { return gofakeit.Price(min, max) }
func (globalSource) ProductName() string            { return gofakeit.ProductName() }
func (globalSource) RandomString(a []string) string { return gofakeit.RandomString(a) }
func (globalSource) Sentence(wordCount int) string  { return gofakeit.Sentence(wordCount) }
func (globalSource) State() string                  { return gofakeit.State() }
func (globalSource) Street() string                 { return gofakeit.Street() }
func (globalSource) URL() string                    { return gofakeit.URL() }
func (globalSource) UUID() string                   { return gofakeit.UUID() }
func (globalSource) Username() string               { return gofakeit.Username() }
func (globalSource) Word() string                   { return gofakeit.Word() }
func (globalSource) Zip() string                    { return gofakeit.Zip() }

// generator generates values from one random source.
type generator struct {
	rnd randomSource
	// shapes deals shaped foreign keys; nil when the run has no shapes.
	shapes *shaper
}

// defaultGen is the single-goroutine generator on the global, seeded source.
var defaultGen = generator{rnd: globalSource{}}

// privateGenerator returns a generator on its own unlocked source, for one
// goroutine only.
func privateGenerator() generator {
	return generator{rnd: gofakeit.NewUnlocked(0)}
}

// Package-level forms on the global source, for single-goroutine callers
// (catalog Evaluate, rules) and tests.

func generate(fakerStr string) (interface{}, error) { return defaultGen.generate(fakerStr) }

func generateValue(col schema.Column, colName, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string) (interface{}, error) {
	return defaultGen.generateValue(col, colName, tableName, generatedPKs, enumVal, enumCol)
}

func generatePK(colType string, existingCount int) (interface{}, error) {
	return defaultGen.generatePK(colType, existingCount)
}

func generateStandardRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int, existingKeys takenKeys) error {
	return defaultGen.generateStandardRows(data, generatedPKs, table, tableName, rows, existingKeys)
}

func generateEnumRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName, enumCol string, enumVals []string, enumRows int, existingKeys takenKeys) error {
	return defaultGen.generateEnumRows(data, generatedPKs, table, tableName, enumCol, enumVals, enumRows, existingKeys)
}

func generateCompositeFKPKRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int, existingKeys takenKeys) (int, bool, error) {
	return defaultGen.generateCompositeFKPKRows(data, generatedPKs, table, tableName, rows, existingKeys)
}

func enumerateCompositeFKPKRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int, existingKeys takenKeys, start int) (int, int, bool, error) {
	return defaultGen.enumerateCompositeFKPKRows(data, generatedPKs, table, tableName, rows, existingKeys, start)
}

func topUpEnumCoverage(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, enumCols map[string][]string, minRows int, existingKeys takenKeys) error {
	return defaultGen.topUpEnumCoverage(data, generatedPKs, table, tableName, enumCols, minRows, existingKeys)
}

func enforceUniqueGroups(rows []map[string]interface{}, table schema.Table, overrides map[string]ColumnOverride, stored map[string]*keySet) ([]map[string]interface{}, map[string]int) {
	return defaultGen.enforceUniqueGroups(rows, table, overrides, stored)
}
