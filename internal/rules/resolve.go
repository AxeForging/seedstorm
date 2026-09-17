package rules

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/brianvoe/gofakeit/v6"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// Source says where a column's effective value comes from.
type Source struct {
	Kind  string `json:"kind"`            // default | column | pattern
	Index int    `json:"index,omitempty"` // pattern rule index when Kind is pattern
	Label string `json:"label"`
}

// Skip records a pattern rule that matched a column but could not apply.
type Skip struct {
	Index  int    `json:"index"`
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// ColumnPlan is the resolved view of one column for builders and dry runs.
type ColumnPlan struct {
	Column    string  `json:"column"`
	Type      string  `json:"type"`
	Default   string  `json:"default"`
	PK        bool    `json:"pk,omitempty"`
	FK        string  `json:"fk,omitempty"`
	Nullable  bool    `json:"nullable,omitempty"`
	Unique    bool    `json:"unique,omitempty"`
	Generated bool    `json:"generated,omitempty"`
	Protected string  `json:"protected,omitempty"`
	Action    *Action `json:"action,omitempty"`
	Effective string  `json:"effective"`
	Source    Source  `json:"source"`
	Skipped   []Skip  `json:"skipped,omitempty"`
}

// protectedReason explains why no rule may touch a column, or "" if one may.
// Keys and generated columns carry integrity that rules must not break.
func protectedReason(col schema.Column) string {
	switch {
	case col.PK:
		return "primary key"
	case col.FK != "":
		return "foreign key to " + col.FK
	case col.Generated:
		return "generated column"
	}
	return ""
}

func globMatch(pattern, name string) bool {
	if pattern == "" {
		pattern = "*"
	}
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	return err == nil && ok
}

func ruleLabel(i int, r Rule) string {
	if r.Name != "" {
		return fmt.Sprintf("#%d %s", i+1, r.Name)
	}
	table := r.Table
	if table == "" {
		table = "*"
	}
	return fmt.Sprintf("#%d %s.%s", i+1, table, r.Column)
}

// incompatibility reports why an action cannot produce values for the column,
// or "" if it can. It evaluates one sample and runs it through the same
// coercion the generator uses, so the answer matches what a run would do.
//
// strict is set for pattern rules: a fixed value aimed at a temporal, uuid or
// json column is only accepted when the author named that column explicitly.
func incompatibility(a Action, col schema.Column, strict bool) string {
	if a.SetNull {
		if !col.Nullable {
			return "column is NOT NULL"
		}
		return ""
	}
	kind := faker.ValueKind(col.Type)
	if a.Template != "" {
		tpl, err := ParseTemplate(a.Template)
		if err != nil {
			return err.Error()
		}
		if tpl.HasLiteral() && kind != faker.KindText {
			return fmt.Sprintf("template adds text but column is %s", col.Type)
		}
	}
	if (a.Value != nil || len(a.OneOf) > 0) && kind == faker.KindOther {
		// Temporal/uuid/json values pass through to the database unchecked.
		if strict {
			return fmt.Sprintf("fixed values need an explicit column rule on %s columns", col.Type)
		}
		return ""
	}
	sample, err := sampleValue(a, col)
	if err != nil {
		return err.Error()
	}
	if _, err := faker.CoerceValue(col, sample); err != nil {
		return err.Error()
	}
	return ""
}

func sampleValue(a Action, col schema.Column) (interface{}, error) {
	auto := sampleAuto(col)
	ev := evaluator(a, "t", "c", "run")
	if ev == nil {
		return nil, fmt.Errorf("invalid action")
	}
	return ev(0, auto)
}

func sampleAuto(col schema.Column) interface{} {
	if col.Faker == "" || col.Faker == "sequence" {
		return 1
	}
	v, err := faker.Evaluate(col.Faker)
	if err != nil {
		return "x"
	}
	return v
}

// foldLookup finds name in m exactly, or else the single key equal to it
// ignoring case that no other entry claims exactly (claimed). Engines disagree
// on identifier case (MySQL USER_ENTITY, Postgres user_entity), and one profile
// has to drive both.
func foldLookup[T any](m map[string]T, name string, claimed func(key string) bool) (string, bool) {
	if _, ok := m[name]; ok {
		return name, true
	}
	found := ""
	for k := range m {
		if !strings.EqualFold(k, name) || claimed(k) {
			continue
		}
		if found != "" {
			return "", false // ambiguous
		}
		found = k
	}
	return found, found != ""
}

// explicitAction returns the explicit rule for a schema table and column, if any.
func (rs *RuleSet) explicitAction(sc *schema.Schema, tableName, colName string) (Action, bool) {
	docTable, ok := foldLookup(rs.Tables, tableName, func(k string) bool {
		_, exact := sc.Tables[k]
		return exact && k != tableName
	})
	if !ok {
		return Action{}, false
	}
	table := sc.Tables[tableName]
	docCol, ok := foldLookup(rs.Tables[docTable].Columns, colName, func(k string) bool {
		_, exact := table.Columns[k]
		return exact && k != colName
	})
	if !ok {
		return Action{}, false
	}
	return rs.Tables[docTable].Columns[docCol], true
}

// schemaTableFor maps a table named in the document to the schema's table.
func (rs *RuleSet) schemaTableFor(sc *schema.Schema, docTable string) (string, bool) {
	return foldLookup(sc.Tables, docTable, func(k string) bool {
		_, exact := rs.Tables[k]
		return exact && k != docTable
	})
}

// resolve picks the action for one column: explicit table column rule first,
// then the first compatible pattern rule.
func (rs *RuleSet) resolve(sc *schema.Schema, tableName, colName string, col schema.Column) (*Action, Source, []Skip) {
	if rs == nil {
		return nil, Source{Kind: "default", Label: "automatic"}, nil
	}
	if a, ok := rs.explicitAction(sc, tableName, colName); ok {
		return &a, Source{Kind: "column", Label: tableName + "." + colName}, nil
	}
	var skips []Skip
	for i, r := range rs.Rules {
		if !globMatch(r.Table, tableName) || !globMatch(r.Column, colName) {
			continue
		}
		if r.Kind() == "" {
			continue
		}
		if reason := protectedReason(col); reason != "" {
			skips = append(skips, Skip{Index: i, Label: ruleLabel(i, r), Reason: reason})
			continue
		}
		if reason := incompatibility(r.Action, col, true); reason != "" {
			skips = append(skips, Skip{Index: i, Label: ruleLabel(i, r), Reason: reason})
			continue
		}
		action := r.Action
		return &action, Source{Kind: "pattern", Index: i, Label: ruleLabel(i, r)}, skips
	}
	return nil, Source{Kind: "default", Label: "automatic"}, skips
}

// Explain resolves every column of a table, sorted by column name.
func (rs *RuleSet) Explain(sc *schema.Schema, tableName string) []ColumnPlan {
	table, ok := sc.Tables[tableName]
	if !ok {
		return nil
	}
	plans := make([]ColumnPlan, 0, len(table.Columns))
	for _, colName := range sortedKeys(table.Columns) {
		col := table.Columns[colName]
		action, source, skips := rs.resolve(sc, tableName, colName, col)
		p := ColumnPlan{
			Column:    colName,
			Type:      col.Type,
			Default:   col.Faker,
			PK:        col.PK,
			FK:        col.FK,
			Nullable:  col.Nullable,
			Unique:    col.Unique,
			Generated: col.Generated,
			Protected: protectedReason(col),
			Action:    action,
			Source:    source,
			Skipped:   skips,
		}
		switch {
		case action != nil:
			p.Effective = action.Summary()
		case p.Protected != "":
			p.Effective = p.Protected
		case col.Faker == "":
			p.Effective = "database default"
		default:
			p.Effective = "faker " + col.Faker
		}
		plans = append(plans, p)
	}
	return plans
}

// Validate checks the rule set structurally and, when sc is non-nil, against a
// concrete schema. Missing tables and columns are warnings so one rules file
// can be shared across databases.
func (rs *RuleSet) Validate(sc *schema.Schema) []Issue {
	issues := rs.validateStructure()
	if sc == nil || HasErrors(issues) {
		return issues
	}
	add := func(sev Severity, p, format string, args ...interface{}) {
		issues = append(issues, Issue{Severity: sev, Path: p, Message: fmt.Sprintf(format, args...)})
	}
	for _, docTable := range sortedKeys(rs.Tables) {
		tableName, ok := rs.schemaTableFor(sc, docTable)
		if !ok {
			add(SeverityWarning, "tables."+docTable, "table %q is not in this database", docTable)
			continue
		}
		table := sc.Tables[tableName]
		docCols := rs.Tables[docTable].Columns
		for _, docCol := range sortedKeys(docCols) {
			p := "tables." + docTable + ".columns." + docCol
			colName, ok := foldLookup(table.Columns, docCol, func(k string) bool {
				_, exact := docCols[k]
				return exact && k != docCol
			})
			if !ok {
				add(SeverityWarning, p, "column %q is not in table %q", docCol, tableName)
				continue
			}
			col := table.Columns[colName]
			a := docCols[docCol]
			if reason := protectedReason(col); reason != "" {
				add(SeverityError, p, "cannot set a %s", reason)
				continue
			}
			if reason := incompatibility(a, col, false); reason != "" {
				add(SeverityError, p, "%s", reason)
				continue
			}
			if msg := uniquenessRisk(a, col); msg != "" {
				add(SeverityWarning, p, "%s", msg)
			}
			if msg := groupRisk(a, table, colName); msg != "" {
				add(SeverityWarning, p, "%s", msg)
			}
		}
	}
	for i, r := range rs.Rules {
		p := fmt.Sprintf("rules[%d]", i)
		applied := 0
		for _, tableName := range sortedKeys(sc.Tables) {
			for _, colName := range sortedKeys(sc.Tables[tableName].Columns) {
				col := sc.Tables[tableName].Columns[colName]
				_, source, _ := rs.resolve(sc, tableName, colName, col)
				if source.Kind == "pattern" && source.Index == i {
					applied++
					if msg := uniquenessRisk(r.Action, col); msg != "" {
						add(SeverityWarning, p, "%s.%s: %s", tableName, colName, msg)
					}
					if msg := groupRisk(r.Action, sc.Tables[tableName], colName); msg != "" {
						add(SeverityWarning, p, "%s.%s: %s", tableName, colName, msg)
					}
				}
			}
		}
		if applied == 0 {
			if earlier := shadowedBy(rs, sc, i); earlier >= 0 {
				add(SeverityWarning, p, "%s matches columns, but every one is already taken by %s; move it up or narrow the earlier rule", ruleLabel(i, r), ruleLabel(earlier, rs.Rules[earlier]))
			} else {
				add(SeverityWarning, p, "%s applies to no column in this database", ruleLabel(i, r))
			}
		}
	}
	issues = append(issues, rs.validateIgnoreSchema(sc)...)
	issues = append(issues, rs.validateRelationshipsSchema(sc)...)
	return issues
}

// shadowedBy returns the earlier pattern rule that claims a column rule i would
// otherwise match, or -1 when rule i matches nothing at all.
func shadowedBy(rs *RuleSet, sc *schema.Schema, i int) int {
	r := rs.Rules[i]
	for _, tableName := range sortedKeys(sc.Tables) {
		if !globMatch(r.Table, tableName) {
			continue
		}
		for _, colName := range sortedKeys(sc.Tables[tableName].Columns) {
			col := sc.Tables[tableName].Columns[colName]
			if !globMatch(r.Column, colName) || protectedReason(col) != "" || incompatibility(r.Action, col, true) != "" {
				continue
			}
			if _, source, _ := rs.resolve(sc, tableName, colName, col); source.Kind == "pattern" && source.Index < i {
				return source.Index
			}
		}
	}
	return -1
}

// groupRisk warns when a fixed value or short list is aimed at a column that is
// part of a multi-column UNIQUE constraint: the other columns must then supply
// all the variety, and rows that cannot be made distinct are dropped.
func groupRisk(a Action, table schema.Table, colName string) string {
	var groups []string
	for _, group := range table.Unique {
		for _, c := range group {
			if c == colName {
				groups = append(groups, "("+strings.Join(group, ", ")+")")
				break
			}
		}
	}
	if len(groups) == 0 {
		return ""
	}
	prefix := "column is part of UNIQUE " + strings.Join(groups, " and ")
	switch a.Kind() {
	case "value", "oneOf":
		return prefix + "; a fixed value or short list limits how many distinct rows fit"
	case "template":
		if tpl, err := ParseTemplate(a.Template); err == nil && seqOnly(tpl) {
			return prefix + "; " + seqRestartHint
		}
	}
	return ""
}

const seqRestartHint = "{{seq}} restarts every run, add {{run}} so later runs (top-ups) do not collide"

// seqOnly reports a template whose only source of distinct values is {{seq}},
// which repeats on every run.
func seqOnly(tpl *Template) bool {
	return tpl.HasToken(TokenSeq) && !tpl.HasToken(TokenRun) && !tpl.HasToken(TokenAuto) && !tpl.HasToken("uuid")
}

func uniquenessRisk(a Action, col schema.Column) string {
	if !col.Unique {
		return ""
	}
	switch a.Kind() {
	case "value", "oneOf":
		return "column is UNIQUE; a fixed value or short list will collide past a few rows"
	case "template":
		tpl, err := ParseTemplate(a.Template)
		if err != nil {
			return ""
		}
		if seqOnly(tpl) {
			return "column is UNIQUE; " + seqRestartHint
		}
		if tpl.HasToken(TokenSeq) || tpl.HasToken(TokenAuto) || tpl.HasToken("uuid") {
			return ""
		}
		return "column is UNIQUE; include {{seq}}, {{auto}} or {{uuid}} to keep values distinct"
	}
	return ""
}

// NewRunID returns the short id rendered by {{run}}.
func NewRunID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "run000"
	}
	return hex.EncodeToString(b)
}

// Compile turns the rule set into generator overrides for sc. It refuses rule
// sets with validation errors. runID is rendered by {{run}}.
func (rs *RuleSet) Compile(sc *schema.Schema, runID string) (faker.Overrides, error) {
	if rs == nil {
		return nil, nil
	}
	issues := rs.Validate(sc)
	if HasErrors(issues) {
		msgs := make([]string, 0)
		for _, i := range issues {
			if i.Severity == SeverityError {
				msgs = append(msgs, i.Path+": "+i.Message)
			}
		}
		return nil, fmt.Errorf("invalid rules: %s", strings.Join(msgs, "; "))
	}
	out := faker.Overrides{}
	for _, tableName := range sortedKeys(sc.Tables) {
		for _, colName := range sortedKeys(sc.Tables[tableName].Columns) {
			col := sc.Tables[tableName].Columns[colName]
			action, _, _ := rs.resolve(sc, tableName, colName, col)
			if action == nil {
				continue
			}
			ev := evaluator(*action, tableName, colName, runID)
			if ev == nil {
				continue
			}
			if out[tableName] == nil {
				out[tableName] = map[string]faker.ColumnOverride{}
			}
			out[tableName][colName] = ev
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func evaluator(a Action, tableName, colName, runID string) faker.ColumnOverride {
	switch a.Kind() {
	case "template":
		tpl, err := ParseTemplate(a.Template)
		if err != nil {
			return nil
		}
		return func(row int, auto interface{}) (interface{}, error) {
			return tpl.Eval(EvalContext{Row: row, Auto: auto, Table: tableName, Column: colName, Run: runID})
		}
	case "faker":
		expr := a.Faker
		return func(int, interface{}) (interface{}, error) { return faker.Evaluate(expr) }
	case "value":
		v := valueString(a.Value)
		return func(int, interface{}) (interface{}, error) { return v, nil }
	case "oneOf":
		values := append([]string(nil), a.OneOf...)
		return func(int, interface{}) (interface{}, error) { return gofakeit.RandomString(values), nil }
	case "setNull":
		return func(int, interface{}) (interface{}, error) { return nil, nil }
	}
	return nil
}

// RuleExample shows sample output of one pattern rule on the first column it
// rewrites. Column is empty when the rule currently applies nowhere.
type RuleExample struct {
	Index  int      `json:"index"`
	Table  string   `json:"table,omitempty"`
	Column string   `json:"column,omitempty"`
	Values []string `json:"values,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// Examples evaluates each pattern rule n times against the first column it
// applies to, looking in the prefer table first (the one the user is viewing),
// then in table, column order.
func (rs *RuleSet) Examples(sc *schema.Schema, n int, prefer string) []RuleExample {
	out := make([]RuleExample, len(rs.Rules))
	for i := range out {
		out[i].Index = i
	}
	found := make([]bool, len(rs.Rules))
	order := sortedKeys(sc.Tables)
	if _, ok := sc.Tables[prefer]; ok {
		order = append([]string{prefer}, order...)
	}
	for _, tableName := range order {
		for _, colName := range sortedKeys(sc.Tables[tableName].Columns) {
			col := sc.Tables[tableName].Columns[colName]
			_, source, _ := rs.resolve(sc, tableName, colName, col)
			if source.Kind != "pattern" || found[source.Index] {
				continue
			}
			found[source.Index] = true
			ex := &out[source.Index]
			ex.Table, ex.Column = tableName, colName
			vals, err := ExampleValues(rs.Rules[source.Index].Action, col, tableName, colName, n)
			ex.Values = vals
			if err != nil {
				ex.Error = err.Error()
			}
		}
	}
	return out
}

// ExampleValues renders n values an action would write into one column, after
// the same coercion a run applies. It refuses what a run would refuse.
func ExampleValues(a Action, col schema.Column, tableName, colName string, n int) ([]string, error) {
	if msgs := actionProblems(a); len(msgs) > 0 {
		return nil, fmt.Errorf("%s", msgs[0])
	}
	if reason := protectedReason(col); reason != "" {
		return nil, fmt.Errorf("cannot set a %s", reason)
	}
	if reason := incompatibility(a, col, false); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	ev := evaluator(a, tableName, colName, "preview")
	values := make([]string, 0, n)
	for i := 0; i < n; i++ {
		v, err := ev(i, sampleAuto(col))
		if err != nil {
			return values, err
		}
		if v, err = faker.CoerceValue(col, v); err != nil {
			return values, err
		}
		if v == nil {
			values = append(values, "NULL")
			continue
		}
		values = append(values, formatValue(v))
	}
	return values, nil
}
