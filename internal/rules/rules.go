// Package rules models user-defined value rules: ordered column patterns and
// per-table column overrides that shape generated data (a prefix on every email,
// a fixed status, NULL for a column) while everything else keeps seedstorm's
// automatic mapping. CLI, TUI and web all compile rules through this package.
package rules

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// Version is the rule file format version written without relationships;
// this build reads up to versionRelationships.
const Version = 1

// RuleSet is a complete rules document.
type RuleSet struct {
	Version     int                   `json:"version" yaml:"version"`
	Name        string                `json:"name,omitempty" yaml:"name,omitempty"`
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	Rules       []Rule                `json:"rules,omitempty" yaml:"rules,omitempty"`
	Tables      map[string]TableRules `json:"tables,omitempty" yaml:"tables,omitempty"`
	// Ignore lists table globs (case-insensitive, path.Match syntax) that no
	// run may write: seed, gaps, generate and mirror all leave them untouched.
	Ignore []string `json:"ignore,omitempty" yaml:"ignore,omitempty"`
	// Relationships shapes foreign keys, keyed by "table.column": how many
	// children each parent gets. A profile with them is version 2.
	Relationships map[string]Relationship `json:"relationships,omitempty" yaml:"relationships,omitempty"`
}

// Rule applies an action to every column matching the table and column globs.
// Pattern rules are evaluated in order; the first compatible match wins.
type Rule struct {
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
	Table  string `json:"table,omitempty" yaml:"table,omitempty"`
	Column string `json:"column" yaml:"column"`
	Action `yaml:",inline"`
}

// TableRules holds explicit settings for one table. Column actions here beat
// any pattern rule.
type TableRules struct {
	Rows    int               `json:"rows,omitempty" yaml:"rows,omitempty"`
	Columns map[string]Action `json:"columns,omitempty" yaml:"columns,omitempty"`
}

// Action is what a rule does to a column. Exactly one field must be set.
type Action struct {
	Template string      `json:"template,omitempty" yaml:"template,omitempty"`
	Faker    string      `json:"faker,omitempty" yaml:"faker,omitempty"`
	Value    interface{} `json:"value,omitempty" yaml:"value,omitempty"`
	OneOf    []string    `json:"oneOf,omitempty" yaml:"oneOf,omitempty"`
	SetNull  bool        `json:"setNull,omitempty" yaml:"setNull,omitempty"`
}

// Kind names the action's single set field, or "" when none or several are set.
func (a Action) Kind() string {
	kinds := a.kinds()
	if len(kinds) != 1 {
		return ""
	}
	return kinds[0]
}

func (a Action) kinds() []string {
	var kinds []string
	if a.Template != "" {
		kinds = append(kinds, "template")
	}
	if a.Faker != "" {
		kinds = append(kinds, "faker")
	}
	if a.Value != nil {
		kinds = append(kinds, "value")
	}
	if len(a.OneOf) > 0 {
		kinds = append(kinds, "oneOf")
	}
	if a.SetNull {
		kinds = append(kinds, "setNull")
	}
	return kinds
}

// Summary renders the action for tables and logs, e.g. `template "ss+{{seq}}"`.
func (a Action) Summary() string {
	switch a.Kind() {
	case "template":
		return fmt.Sprintf("template %q", a.Template)
	case "faker":
		return "faker " + a.Faker
	case "value":
		return fmt.Sprintf("value %q", valueString(a.Value))
	case "oneOf":
		return "one of " + strings.Join(a.OneOf, ", ")
	case "setNull":
		return "NULL"
	}
	return "invalid action"
}

// Parse decodes a YAML (or JSON, a YAML subset) rules document.
func Parse(data []byte) (*RuleSet, error) {
	rs := &RuleSet{}
	if strings.TrimSpace(string(data)) == "" {
		rs.Version = Version
		return rs, nil
	}
	if err := yaml.Unmarshal(data, rs); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	if rs.Version == 0 {
		rs.Version = Version
	}
	return rs, nil
}

// Load reads a rules file from disk.
func Load(filePath string) (*RuleSet, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read rules file %s: %w", filePath, err)
	}
	rs, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	return rs, nil
}

// Marshal encodes the rule set as YAML.
func (rs *RuleSet) Marshal() ([]byte, error) {
	out := *rs
	out.Version = FormatVersion(&out)
	return yaml.Marshal(out)
}

// MergeTableRowsFor overlays explicit per-table counts on top of the rule set's
// counts, keyed by the schema's own table names (a profile's `user_entity`
// sizes MySQL's USER_ENTITY). A count given on the command line or in the UI
// wins.
func MergeTableRowsFor(rs *RuleSet, sc *schema.Schema, explicit map[string]int) map[string]int {
	merged := make(map[string]int)
	if rs != nil {
		for docTable, t := range rs.Tables {
			if t.Rows <= 0 {
				continue
			}
			name := docTable
			if sc != nil {
				if mapped, ok := rs.schemaTableFor(sc, docTable); ok {
					name = mapped
				}
			}
			merged[name] = t.Rows
		}
	}
	for k, v := range explicit {
		if v > 0 {
			merged[k] = v
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// Severity grades a validation issue.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one validation finding. Path points into the document, e.g.
// "rules[2]" or "tables.users.columns.email".
type Issue struct {
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`
	Message  string   `json:"message"`
}

func (i Issue) String() string {
	return fmt.Sprintf("%s: %s: %s", i.Severity, i.Path, i.Message)
}

// HasErrors reports whether any issue blocks use of the rule set.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// validateStructure checks everything that does not need a schema.
func (rs *RuleSet) validateStructure() []Issue {
	var issues []Issue
	add := func(sev Severity, p, format string, args ...interface{}) {
		issues = append(issues, Issue{Severity: sev, Path: p, Message: fmt.Sprintf(format, args...)})
	}
	if rs.Version > versionRelationships {
		add(SeverityError, "version", "version %d is newer than this seedstorm supports (%d)", rs.Version, versionRelationships)
	}
	for i, r := range rs.Rules {
		p := fmt.Sprintf("rules[%d]", i)
		if strings.TrimSpace(r.Column) == "" {
			add(SeverityError, p, "column pattern is required")
		} else if _, err := path.Match(r.Column, "x"); err != nil {
			add(SeverityError, p, "invalid column pattern %q", r.Column)
		}
		if r.Table != "" {
			if _, err := path.Match(r.Table, "x"); err != nil {
				add(SeverityError, p, "invalid table pattern %q", r.Table)
			}
		}
		for _, msg := range actionProblems(r.Action) {
			add(SeverityError, p, "%s", msg)
		}
	}
	for _, tableName := range sortedKeys(rs.Tables) {
		t := rs.Tables[tableName]
		if t.Rows < 0 {
			add(SeverityError, "tables."+tableName+".rows", "rows must be zero or positive")
		}
		for _, colName := range sortedKeys(t.Columns) {
			for _, msg := range actionProblems(t.Columns[colName]) {
				add(SeverityError, "tables."+tableName+".columns."+colName, "%s", msg)
			}
		}
	}
	issues = append(issues, rs.validateIgnoreStructure()...)
	issues = append(issues, rs.validateRelationshipsStructure()...)
	return issues
}

func actionProblems(a Action) []string {
	kinds := a.kinds()
	switch len(kinds) {
	case 0:
		return []string{"set one of template, faker, value, oneOf or setNull"}
	case 1:
	default:
		return []string{"set only one of " + strings.Join(kinds, ", ")}
	}
	switch kinds[0] {
	case "template":
		if _, err := ParseTemplate(a.Template); err != nil {
			return []string{err.Error()}
		}
	case "faker":
		if _, err := faker.Evaluate(a.Faker); err != nil {
			return []string{err.Error()}
		}
	}
	return nil
}

func valueString(v interface{}) string {
	return formatValue(v)
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
