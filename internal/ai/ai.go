package ai

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
)

// EnrichFakerMappings uses Gemini to produce semantically meaningful faker mappings
// for columns that lack one or have a generic fallback.
// model is the Gemini model ID (e.g. "gemini-2.5-flash", "gemini-1.5-pro").
// appContext is an optional free-text hint describing the application domain
// (e.g. "TacoShop", "HR management system") that is injected into every prompt.
func EnrichFakerMappings(ctx context.Context, s *schema.Schema, model, appContext string) (*schema.Schema, string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, "", fmt.Errorf("GEMINI_API_KEY environment variable is not set")
	}

	llm, err := googleai.New(ctx, googleai.WithAPIKey(apiKey), googleai.WithDefaultModel(model))
	if err != nil {
		return nil, "", fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// Collect all table names for context
	tableNames := make([]string, 0, len(s.Tables))
	for name := range s.Tables {
		tableNames = append(tableNames, name)
	}
	tableContext := strings.Join(tableNames, ", ")

	for tableName, table := range s.Tables {
		// Build column context for this table
		colNames := make([]string, 0, len(table.Columns))
		for colName := range table.Columns {
			colNames = append(colNames, colName)
		}
		colContext := strings.Join(colNames, ", ")

		for colName, col := range table.Columns {
			if col.Faker != "" && col.Faker != "word" && col.Faker != "sentence" {
				continue // already has a meaningful mapping
			}
			if col.PK || col.FK != "" {
				continue // managed by the seed engine
			}

			prompt := buildPrompt(tableName, colName, col.Type, colContext, tableContext, appContext)
			answer, err := llms.GenerateFromSinglePrompt(ctx, llm, prompt)
			if err != nil {
				return nil, "", fmt.Errorf("AI call failed for %s.%s: %w", tableName, colName, err)
			}

			c := s.Tables[tableName].Columns[colName]
			c.Faker = cleanFakerString(answer)
			tbl := s.Tables[tableName]
			tbl.Columns[colName] = c
			s.Tables[tableName] = tbl
		}
	}

	return s, model, nil
}

// buildPrompt constructs a context-rich prompt for Gemini.
// appContext is an optional free-text hint about the application domain (may be empty).
// When appContext is provided and the column is a string type, the AI is also allowed
// to return randomstring(val1,val2,...) with domain-specific values.
func buildPrompt(tableName, colName, colType, siblingCols, allTables, appContext string) string {
	domainLine := ""
	if appContext != "" {
		domainLine = fmt.Sprintf("- Application domain / context: %s\n", appContext)
	}

	randomstringRule := ""
	if appContext != "" && isStringType(colType) {
		randomstringRule = fmt.Sprintf(`
DOMAIN VALUES OPTION (preferred for entity names when domain context is given):
If the column represents a domain-specific entity name or label (e.g. product name, category name,
tag name, dish name, ingredient, etc.) you may return:
  randomstring(Value One,Value Two,Value Three,Value Four,Value Five,Value Six,Value Seven,Value Eight)
Generate 6-10 realistic values that fit the "%s" domain. No quotes around values. Comma-separated.
Example for a taco shop product name: randomstring(Taco al Pastor,Burrito de Carnitas,Quesadilla de Pollo,Elote Asado,Guacamole,Salsa Verde,Chile Relleno,Enchiladas Verdes)
Do NOT use randomstring for personal data (emails, names, phones), UUIDs, descriptions, or long text — use gofakeit functions for those.
`, appContext)
	}

	returnLine := "Return ONLY the gofakeit function name or call, nothing else"
	if randomstringRule != "" {
		returnLine = "Return ONLY the gofakeit function call OR a randomstring(...) with domain values, nothing else"
	}

	return fmt.Sprintf(`You are a database seeding expert. Given a database column, return the most appropriate faker for generating realistic fake data.

Database context:
- All tables: %s
- Current table: %s
- Columns in this table: %s
- Column name: %s
- Column type: %s
%s
Rules:
- %s
- Use lowercase, no "gofakeit." prefix
- Valid gofakeit functions: name, firstname, lastname, email, phone, username, street, city, state, country, zip, url, uuid, company, jobtitle, productname, word, sentence, datetime, date, bool, ipv4, hexcolor, latitude, longitude, price(min,max), number(min,max), paragraph(n)
- CRITICAL: match return type to column type. If column type contains "char", "text", or "varchar" → use a string-returning function NOT number() or price()
- Choose based on what the column SEMANTICALLY represents in a %s table context
- Examples: tracking_number(varchar) → uuid, brand name(varchar) → company, review body(text) → paragraph(2), coupon code(varchar) → word
%s
Return only the faker:`, allTables, tableName, siblingCols, colName, colType, domainLine, returnLine, tableName, randomstringRule)
}

// isStringType returns true if the DB column type is a string-like type.
func isStringType(colType string) bool {
	t := strings.ToLower(colType)
	return strings.Contains(t, "char") || strings.Contains(t, "text") ||
		t == "clob" || t == "tinytext" || t == "mediumtext" || t == "longtext"
}

func cleanFakerString(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "gofakeit.")
	// Remove markdown code block formatting if present
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// For randomstring(...) keep the values intact but strip any surrounding
	// quotes the AI may have added around individual values.
	if strings.HasPrefix(s, "randomstring(") {
		return cleanRandomString(s)
	}

	// For plain gofakeit functions remove quotes and backticks that the AI
	// sometimes wraps the whole response in.
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "`", "")
	// Strip bare parentheses — e.g. "company()" → "company", "street()" → "street"
	// but keep parameterised calls like "price(1,1000)", "number(0,500)"
	s = strings.TrimSuffix(s, "()")
	return s
}

// cleanRandomString normalises a randomstring(val1,val2,...) response from the AI.
// It strips surrounding quotes from individual values (the AI often wraps them)
// and trims whitespace around each value.
func cleanRandomString(s string) string {
	// Extract content between first '(' and last ')'
	open := strings.Index(s, "(")
	close := strings.LastIndex(s, ")")
	if open < 0 || close <= open {
		return s
	}
	inner := s[open+1 : close]
	parts := strings.Split(inner, ",")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`+"`")
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	if len(cleaned) == 0 {
		return s
	}
	return "randomstring(" + strings.Join(cleaned, ",") + ")"
}
