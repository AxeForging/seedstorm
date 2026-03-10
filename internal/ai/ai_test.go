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
	if strings.Contains(prompt, "randomstring") {
		t.Errorf("expected prompt to omit randomstring domain-values option when context is empty, got:\n%s", prompt)
	}
}

func TestBuildPrompt_DomainValuesOption_StringColumn(t *testing.T) {
	// varchar + appContext → randomstring option should appear
	prompt := buildPrompt("products", "name", "varchar", "id,name,price", "products,users", "Mexican taco shop")
	if !strings.Contains(prompt, "randomstring(") {
		t.Errorf("expected prompt to contain randomstring domain-values option for varchar+appContext, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Mexican taco shop") {
		t.Errorf("expected prompt to contain domain context value")
	}
}

func TestBuildPrompt_DomainValuesOption_NonStringColumn(t *testing.T) {
	// integer + appContext → randomstring option should NOT appear (wrong type)
	prompt := buildPrompt("products", "stock", "integer", "id,name,stock", "products,users", "TacoShop")
	if strings.Contains(prompt, "DOMAIN VALUES OPTION") {
		t.Errorf("expected randomstring domain-values option to be omitted for non-string column")
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
