package ai

import (
	"strings"
	"testing"
)

func TestBuildPrompt_WithAppContext(t *testing.T) {
	prompt := buildPrompt("products", "name", "varchar", "id,name,price", "products,users", "TacoShop")
	if !strings.Contains(prompt, "TacoShop") {
		t.Errorf("expected prompt to contain app context 'TacoShop', got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Application domain") {
		t.Errorf("expected prompt to contain 'Application domain' label, got:\n%s", prompt)
	}
}

func TestBuildPrompt_WithoutAppContext(t *testing.T) {
	prompt := buildPrompt("products", "name", "varchar", "id,name,price", "products,users", "")
	if strings.Contains(prompt, "Application domain") {
		t.Errorf("expected prompt to omit 'Application domain' line when context is empty, got:\n%s", prompt)
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
