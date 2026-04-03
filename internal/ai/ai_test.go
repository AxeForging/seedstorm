package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

// mockLLM implements llms.Model and fails failCount times before succeeding.
type mockLLM struct {
	calls     int
	failCount int
	answer    string
}

func (m *mockLLM) GenerateContent(_ context.Context, _ []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	m.calls++
	if m.calls <= m.failCount {
		return nil, fmt.Errorf("transient error (call %d)", m.calls)
	}
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: m.answer}},
	}, nil
}

func (m *mockLLM) Call(_ context.Context, _ string, _ ...llms.CallOption) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// ── generateWithRetry ────────────────────────────────────────────────────────

func TestGenerateWithRetry_succeedsFirstTry(t *testing.T) {
	retryBaseDelay = time.Millisecond // fast tests
	defer func() { retryBaseDelay = time.Second }()

	m := &mockLLM{failCount: 0, answer: "email"}
	result, err := generateWithRetry(context.Background(), m, "test", "users.email")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "email" {
		t.Errorf("got %q, want %q", result, "email")
	}
	if m.calls != 1 {
		t.Errorf("expected 1 call, got %d", m.calls)
	}
}

func TestGenerateWithRetry_succeedsAfterRetries(t *testing.T) {
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = time.Second }()

	m := &mockLLM{failCount: 2, answer: "firstname"}
	result, err := generateWithRetry(context.Background(), m, "test", "users.name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "firstname" {
		t.Errorf("got %q, want %q", result, "firstname")
	}
	if m.calls != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", m.calls)
	}
}

func TestGenerateWithRetry_failsAfterMaxRetries(t *testing.T) {
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = time.Second }()

	m := &mockLLM{failCount: 10, answer: "never"}
	_, err := generateWithRetry(context.Background(), m, "test", "users.bio")
	if err == nil {
		t.Fatal("expected error after max retries, got nil")
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("error should mention 3 attempts: %v", err)
	}
	if m.calls != 3 {
		t.Errorf("expected exactly 3 calls, got %d", m.calls)
	}
}

func TestGenerateWithRetry_respectsContextCancellation(t *testing.T) {
	retryBaseDelay = time.Second // real delay so cancellation triggers first
	defer func() { retryBaseDelay = time.Second }()

	ctx, cancel := context.WithCancel(context.Background())
	m := &mockLLM{failCount: 10, answer: "never"}

	// Cancel immediately after the first failure's backoff starts
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := generateWithRetry(ctx, m, "test", "users.x")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestBuildPrompt_WithAppContext(t *testing.T) {
	prompt := buildPrompt("products", "name", "varchar", "id,name,price", "products,users", "TacoShop", nil)
	if !strings.Contains(prompt, "TacoShop") {
		t.Errorf("expected prompt to contain app context 'TacoShop', got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Application domain") {
		t.Errorf("expected prompt to contain 'Application domain' label, got:\n%s", prompt)
	}
}

func TestBuildPrompt_WithoutAppContext(t *testing.T) {
	prompt := buildPrompt("products", "name", "varchar", "id,name,price", "products,users", "", nil)
	if strings.Contains(prompt, "Application domain") {
		t.Errorf("expected prompt to omit 'Application domain' line when context is empty, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "randomstring") {
		t.Errorf("expected prompt to omit randomstring domain-values option when context is empty, got:\n%s", prompt)
	}
}

func TestBuildPrompt_DomainValuesOption_StringColumn(t *testing.T) {
	// varchar + appContext → randomstring option with 20-30 values guidance should appear
	prompt := buildPrompt("products", "name", "varchar", "id,name,price", "products,users", "Mexican taco shop", nil)
	if !strings.Contains(prompt, "randomstring(") {
		t.Errorf("expected prompt to contain randomstring domain-values option for varchar+appContext, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Mexican taco shop") {
		t.Errorf("expected prompt to contain domain context value")
	}
	if !strings.Contains(prompt, "20-30") {
		t.Errorf("expected prompt to ask for 20-30 distinct values for better variety")
	}
}

func TestBuildPrompt_DomainValuesOption_NonStringColumn(t *testing.T) {
	// integer + appContext → randomstring option should NOT appear (wrong type)
	prompt := buildPrompt("products", "stock", "integer", "id,name,stock", "products,users", "TacoShop", nil)
	if strings.Contains(prompt, "DOMAIN VALUES OPTION") {
		t.Errorf("expected randomstring domain-values option to be omitted for non-string column")
	}
}

// ── Batch prompt and response parsing ────────────────────────────────────────

func TestBuildBatchPrompt_containsAllColumns(t *testing.T) {
	cols := []enrichCol{
		{name: "email", colType: "varchar"},
		{name: "age", colType: "integer"},
		{name: "bio", colType: "text"},
	}
	prompt := buildBatchPrompt("users", cols, "id,email,age,bio", "users,orders", "SaaS app")
	for _, col := range cols {
		if !strings.Contains(prompt, col.name) {
			t.Errorf("expected prompt to contain column %q", col.name)
		}
	}
	if !strings.Contains(prompt, "JSON") {
		t.Error("expected prompt to mention JSON return format")
	}
}

func TestParseBatchResponse_validJSON(t *testing.T) {
	resp := `{"email": "email", "age": "number(18,90)", "bio": "paragraph(2)"}`
	m, err := parseBatchResponse(resp)
	if err != nil {
		t.Fatalf("parseBatchResponse: %v", err)
	}
	if m["email"] != "email" || m["age"] != "number(18,90)" || m["bio"] != "paragraph(2)" {
		t.Errorf("unexpected mapping: %v", m)
	}
}

func TestParseBatchResponse_markdownFenced(t *testing.T) {
	resp := "```json\n{\"name\": \"firstname\", \"city\": \"city\"}\n```"
	m, err := parseBatchResponse(resp)
	if err != nil {
		t.Fatalf("parseBatchResponse: %v", err)
	}
	if m["name"] != "firstname" || m["city"] != "city" {
		t.Errorf("unexpected mapping: %v", m)
	}
}

func TestParseBatchResponse_invalidJSON(t *testing.T) {
	_, err := parseBatchResponse("not json at all")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ── Sibling faker context ───────────────────────────────────────────────────

func TestBuildPrompt_SiblingFakerContext(t *testing.T) {
	siblings := map[string]string{
		"email":  "email",
		"status": "randomstring(active,inactive,suspended)",
		"age":    "number(18,90)",
	}
	prompt := buildPrompt("users", "bio", "text", "id,email,status,age,bio", "users,orders", "", siblings)
	if !strings.Contains(prompt, "Sibling column mappings") {
		t.Error("expected prompt to contain sibling context section")
	}
	if !strings.Contains(prompt, "email → email") {
		t.Error("expected prompt to show email sibling mapping")
	}
	if !strings.Contains(prompt, "randomstring(active,inactive,suspended)") {
		t.Error("expected prompt to show status constraint values")
	}
}

func TestBuildPrompt_SiblingContext_ExcludesCurrentColumn(t *testing.T) {
	siblings := map[string]string{
		"name": "word", // current column being enriched — should be excluded
		"age":  "number(18,90)",
	}
	prompt := buildPrompt("users", "name", "varchar", "id,name,age", "users", "", siblings)
	// Should NOT show "name → word" since that's the column being enriched
	if strings.Contains(prompt, "name → word") {
		t.Error("prompt should not include the current column in sibling context")
	}
	if !strings.Contains(prompt, "age → number(18,90)") {
		t.Error("expected prompt to show age sibling mapping")
	}
}

func TestCleanFakerString(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"productname", "productname"},
		{"gofakeit.Company()", "Company"},
		{"```uuid```", "uuid"},
		{"  email  ", "email"},
		{"price(1,1000)", "price(1,1000)"},
		{"company()", "company"},
	}
	for _, tt := range tests {
		got := cleanFakerString(tt.in)
		if got != tt.want {
			t.Errorf("cleanFakerString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCleanRandomString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare values",
			in:   "randomstring(Taco al Pastor,Burrito de Carnitas,Guacamole)",
			want: "randomstring(Taco al Pastor,Burrito de Carnitas,Guacamole)",
		},
		{
			name: "AI wraps values in single quotes",
			in:   "randomstring('Taco al Pastor','Burrito de Carnitas','Guacamole')",
			want: "randomstring(Taco al Pastor,Burrito de Carnitas,Guacamole)",
		},
		{
			name: "AI wraps values in double quotes",
			in:   `randomstring("Taco al Pastor","Burrito de Carnitas","Guacamole")`,
			want: "randomstring(Taco al Pastor,Burrito de Carnitas,Guacamole)",
		},
		{
			name: "extra whitespace around values",
			in:   "randomstring( Taco al Pastor , Burrito de Carnitas , Guacamole )",
			want: "randomstring(Taco al Pastor,Burrito de Carnitas,Guacamole)",
		},
		{
			name: "cleanFakerString dispatches correctly",
			in:   "randomstring(admin,user,guest)",
			want: "randomstring(admin,user,guest)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanFakerString(tt.in)
			if got != tt.want {
				t.Errorf("cleanFakerString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
