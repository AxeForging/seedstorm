package rules

import (
	"fmt"
	"math"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/relations"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// versionRelationships is the format version a profile with relationships
// needs: older binaries refuse it instead of seeding it unshaped.
const versionRelationships = 2

// Relationship is the children-per-parent shape one foreign key is seeded with.
type Relationship struct {
	Min       int                 `json:"min" yaml:"min"`
	Avg       float64             `json:"avg" yaml:"avg"`
	Max       int                 `json:"max" yaml:"max"`
	ZeroShare float64             `json:"zeroShare,omitempty" yaml:"zeroShare,omitempty"`
	NullShare float64             `json:"nullShare,omitempty" yaml:"nullShare,omitempty"`
	Histogram []faker.ShapeBucket `json:"histogram,omitempty" yaml:"histogram,omitempty"`
}

func (r Relationship) shape() faker.Shape {
	return faker.Shape{Min: r.Min, Avg: r.Avg, Max: r.Max, ZeroShare: r.ZeroShare, NullShare: r.NullShare, Histogram: r.Histogram}
}

// RelationshipsFromShapes turns measured shapes (a snapshot, a compare) into
// profile relationships. Shapes without a known maximum (estimates that could
// not read one, failed scans) are skipped and named in skipped.
func RelationshipsFromShapes(shapes []relations.Shape) (out map[string]Relationship, skipped []string) {
	out = map[string]Relationship{}
	for _, s := range shapes {
		key := s.Child + "." + s.Column
		measured := s.Outcome == db.OutcomeOK || s.Outcome == relations.OutcomeEstimated || s.Outcome == relations.OutcomeSkippedUnindexed
		if !measured || s.Max <= 0 || s.SelfRef {
			skipped = append(skipped, key)
			continue
		}
		r := Relationship{Min: int(max(s.Min, 1)), Avg: math.Round(s.Avg*100) / 100, Max: int(s.Max), ZeroShare: round4(s.ZeroShare), NullShare: round4(s.NullShare)}
		for _, b := range s.Histogram {
			r.Histogram = append(r.Histogram, faker.ShapeBucket{Min: int(b.Min), Max: int(b.Max), Parents: b.Parents})
		}
		out[key] = r
	}
	return out, skipped
}

func round4(f float64) float64 { return math.Round(max(f, 0)*10000) / 10000 }

// splitRelationshipKey splits "table.column".
func splitRelationshipKey(key string) (string, string, bool) {
	i := strings.LastIndex(key, ".")
	if i <= 0 || i == len(key)-1 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

func (rs *RuleSet) validateRelationshipsStructure() []Issue {
	var issues []Issue
	add := func(p, format string, args ...interface{}) {
		issues = append(issues, Issue{Severity: SeverityError, Path: p, Message: fmt.Sprintf(format, args...)})
	}
	for _, key := range sortedKeys(rs.Relationships) {
		r := rs.Relationships[key]
		p := "relationships." + key
		if _, _, ok := splitRelationshipKey(key); !ok {
			add(p, "name a relationship as table.column")
			continue
		}
		switch {
		case r.Min < 0 || r.Max < 1:
			add(p, "min must be zero or more and max at least 1")
		case r.Max < r.Min:
			add(p, "max (%d) is below min (%d)", r.Max, r.Min)
		case r.Avg != 0 && (r.Avg < float64(max(r.Min, 1)) || r.Avg > float64(r.Max)):
			add(p, "avg %.2f is outside min %d … max %d", r.Avg, max(r.Min, 1), r.Max)
		}
		if r.ZeroShare < 0 || r.ZeroShare >= 1 || r.NullShare < 0 || r.NullShare >= 1 {
			add(p, "zeroShare and nullShare are shares: 0 or more and below 1")
		}
		for i, b := range r.Histogram {
			if b.Min < 1 || b.Max < b.Min || b.Parents < 0 {
				add(fmt.Sprintf("%s.histogram[%d]", p, i), "a bucket needs 1 ≤ min ≤ max and parents ≥ 0")
			}
		}
	}
	return issues
}

// FormatVersion is the version a rule set is written with: 2 when it has
// relationships, otherwise 1.
func FormatVersion(rs *RuleSet) int {
	if rs != nil && len(rs.Relationships) > 0 {
		return versionRelationships
	}
	return Version
}

// relationshipTarget maps a document key to the schema's table and column.
func (rs *RuleSet) relationshipTarget(sc *schema.Schema, key string) (string, string, bool) {
	docTable, docCol, ok := splitRelationshipKey(key)
	if !ok {
		return "", "", false
	}
	tableName, ok := foldLookup(sc.Tables, docTable, func(string) bool { return false })
	if !ok {
		return "", "", false
	}
	colName, ok := foldLookup(sc.Tables[tableName].Columns, docCol, func(string) bool { return false })
	return tableName, colName, ok
}

func (rs *RuleSet) validateRelationshipsSchema(sc *schema.Schema) []Issue {
	var issues []Issue
	for _, key := range sortedKeys(rs.Relationships) {
		p := "relationships." + key
		tableName, colName, ok := rs.relationshipTarget(sc, key)
		if !ok {
			issues = append(issues, Issue{Severity: SeverityWarning, Path: p, Message: fmt.Sprintf("%s is not in this database", key)})
			continue
		}
		if reason := faker.ShapeSkipReason(sc, tableName, colName); reason != "" {
			issues = append(issues, Issue{Severity: SeverityWarning, Path: p, Message: "not shaped: " + reason})
		}
	}
	return issues
}

// Shapes compiles the profile's relationships for sc, keyed by the schema's
// own names. Keys missing from the database or not shapeable are left out
// (Validate reports them).
func (rs *RuleSet) Shapes(sc *schema.Schema) map[string]faker.Shape {
	if rs == nil || len(rs.Relationships) == 0 || sc == nil {
		return nil
	}
	out := map[string]faker.Shape{}
	for key, r := range rs.Relationships {
		tableName, colName, ok := rs.relationshipTarget(sc, key)
		if !ok || faker.ShapeSkipReason(sc, tableName, colName) != "" {
			continue
		}
		out[faker.ShapeKey(tableName, colName)] = r.shape()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
