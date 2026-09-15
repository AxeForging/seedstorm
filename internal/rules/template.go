package rules

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/faker"
)

// Built-in template tokens. Anything else inside {{ }} is a generator
// expression from the faker catalog.
const (
	TokenAuto   = "auto"
	TokenSeq    = "seq"
	TokenTable  = "table"
	TokenColumn = "column"
	TokenRun    = "run"
)

// Token describes a template token for pickers.
type Token struct {
	Token       string `json:"token"`
	Description string `json:"description"`
}

// BuiltinTokens lists the non-generator tokens a template may use.
func BuiltinTokens() []Token {
	return []Token{
		{TokenAuto, "The value seedstorm would generate for this column"},
		{TokenSeq, "Row number within this run (1, 2, 3, ...)"},
		{TokenRun, "Short id shared by every row of one run"},
		{TokenTable, "Table name"},
		{TokenColumn, "Column name"},
	}
}

// EvalContext carries per-cell inputs for a template.
type EvalContext struct {
	Row    int // 0-based
	Auto   interface{}
	Table  string
	Column string
	Run    string
}

type segment struct {
	literal string
	token   string // empty for literal segments
}

// Template is a parsed value template such as "loadtest+{{seq}}.{{auto}}".
type Template struct {
	raw      string
	segments []segment
}

// ParseTemplate splits text into literal and {{token}} segments and checks
// every token is a builtin or a known generator.
func ParseTemplate(raw string) (*Template, error) {
	t := &Template{raw: raw}
	rest := raw
	for len(rest) > 0 {
		open := strings.Index(rest, "{{")
		closeIdx := strings.Index(rest, "}}")
		if open < 0 {
			if closeIdx >= 0 {
				return nil, fmt.Errorf("unmatched \"}}\" in template %q", raw)
			}
			t.segments = append(t.segments, segment{literal: rest})
			break
		}
		if closeIdx >= 0 && closeIdx < open {
			return nil, fmt.Errorf("unmatched \"}}\" in template %q", raw)
		}
		if open > 0 {
			t.segments = append(t.segments, segment{literal: rest[:open]})
		}
		end := strings.Index(rest[open+2:], "}}")
		if end < 0 {
			return nil, fmt.Errorf("unclosed \"{{\" in template %q", raw)
		}
		token := strings.TrimSpace(rest[open+2 : open+2+end])
		if token == "" {
			return nil, fmt.Errorf("empty {{ }} in template %q", raw)
		}
		if !isBuiltinToken(token) {
			if _, err := faker.Evaluate(token); err != nil {
				return nil, fmt.Errorf("template %q: %w", raw, err)
			}
		}
		t.segments = append(t.segments, segment{token: token})
		rest = rest[open+2+end+2:]
	}
	return t, nil
}

func isBuiltinToken(token string) bool {
	switch token {
	case TokenAuto, TokenSeq, TokenTable, TokenColumn, TokenRun:
		return true
	}
	return false
}

// HasToken reports whether the template uses the given token.
func (t *Template) HasToken(token string) bool {
	for _, s := range t.segments {
		if s.token == token {
			return true
		}
	}
	return false
}

// HasLiteral reports whether the template contains fixed text, which makes its
// output a string regardless of the tokens used.
func (t *Template) HasLiteral() bool {
	for _, s := range t.segments {
		if s.token == "" && s.literal != "" {
			return true
		}
	}
	return false
}

// Eval renders the template for one cell. A template that is exactly one token
// returns that token's native value, so "{{number(1,9)}}" stays an integer.
func (t *Template) Eval(ctx EvalContext) (interface{}, error) {
	if len(t.segments) == 1 && t.segments[0].token != "" {
		return t.tokenValue(t.segments[0].token, ctx)
	}
	var sb strings.Builder
	for _, s := range t.segments {
		if s.token == "" {
			sb.WriteString(s.literal)
			continue
		}
		v, err := t.tokenValue(s.token, ctx)
		if err != nil {
			return nil, err
		}
		sb.WriteString(formatValue(v))
	}
	return sb.String(), nil
}

func (t *Template) tokenValue(token string, ctx EvalContext) (interface{}, error) {
	switch token {
	case TokenAuto:
		return ctx.Auto, nil
	case TokenSeq:
		return ctx.Row + 1, nil
	case TokenTable:
		return ctx.Table, nil
	case TokenColumn:
		return ctx.Column, nil
	case TokenRun:
		return ctx.Run, nil
	}
	return faker.Evaluate(token)
}

// formatValue renders a token value inside surrounding text.
func formatValue(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case time.Time:
		return x.UTC().Format("2006-01-02 15:04:05")
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}
