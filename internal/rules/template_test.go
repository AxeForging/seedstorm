package rules

import (
	"strings"
	"testing"
	"time"
)

func TestParseTemplate_Errors(t *testing.T) {
	for _, raw := range []string{"a{{", "a}}b", "{{}}", "x{{ unknown }}", "}}{{seq}}"} {
		if _, err := ParseTemplate(raw); err == nil {
			t.Errorf("ParseTemplate(%q) = nil error", raw)
		}
	}
}

func TestTemplateEval_BuiltinsAndGenerators(t *testing.T) {
	tpl, err := ParseTemplate("{{table}}.{{column}}#{{seq}}/{{run}}:{{auto}}|{{number(5,5)}}")
	if err != nil {
		t.Fatal(err)
	}
	got, err := tpl.Eval(EvalContext{Row: 2, Auto: "orig", Table: "users", Column: "email", Run: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "users.email#3/r1:orig|5" {
		t.Fatalf("Eval = %q", got)
	}
}

func TestTemplateEval_SingleTokenKeepsNativeType(t *testing.T) {
	tpl, _ := ParseTemplate("{{number(7,7)}}")
	v, err := tpl.Eval(EvalContext{})
	if err != nil {
		t.Fatal(err)
	}
	if v != 7 {
		t.Fatalf("single token = %#v, want int 7", v)
	}
	auto, _ := ParseTemplate("{{auto}}")
	if v, _ := auto.Eval(EvalContext{Auto: nil}); v != nil {
		t.Fatalf("{{auto}} of NULL = %#v, want nil", v)
	}
}

func TestTemplateEval_FormatsAutoValuesInsideText(t *testing.T) {
	tpl, _ := ParseTemplate("at {{auto}}")
	ts := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)
	if v, _ := tpl.Eval(EvalContext{Auto: ts}); v != "at 2020-05-06 07:08:09" {
		t.Fatalf("time = %q", v)
	}
	if v, _ := tpl.Eval(EvalContext{Auto: 2.5}); v != "at 2.5" {
		t.Fatalf("float = %q", v)
	}
	if v, _ := tpl.Eval(EvalContext{Auto: nil}); v != "at " {
		t.Fatalf("nil = %q", v)
	}
}

func TestTemplate_LiteralOnlyAndTokenDetection(t *testing.T) {
	lit, err := ParseTemplate("constant")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := lit.Eval(EvalContext{}); v != "constant" || !lit.HasLiteral() {
		t.Fatalf("literal template = %v", v)
	}
	tok, _ := ParseTemplate("{{seq}}")
	if tok.HasLiteral() || !tok.HasToken(TokenSeq) || tok.HasToken(TokenAuto) {
		t.Fatal("token detection wrong for {{seq}}")
	}
}

func TestBuiltinTokens_AllParse(t *testing.T) {
	for _, tok := range BuiltinTokens() {
		if _, err := ParseTemplate("{{" + tok.Token + "}}"); err != nil {
			t.Errorf("builtin %q: %v", tok.Token, err)
		}
		if strings.TrimSpace(tok.Description) == "" {
			t.Errorf("builtin %q has no description", tok.Token)
		}
	}
}
