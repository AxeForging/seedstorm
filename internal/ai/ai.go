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
func EnrichFakerMappings(ctx context.Context, s *schema.Schema, model string) (*schema.Schema, string, error) {
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

			prompt := buildPrompt(tableName, colName, col.Type, colContext, tableContext)
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
func buildPrompt(tableName, colName, colType, siblingCols, allTables string) string {
	return fmt.Sprintf(`You are a database seeding expert. Given a database column, return the most appropriate gofakeit function call for generating realistic fake data.

Database context:
- All tables: %s
- Current table: %s
- Columns in this table: %s
- Column name: %s
- Column type: %s

Rules:
- Return ONLY the function name or function call, nothing else
- Use lowercase, no "gofakeit." prefix
- Valid functions: name, firstname, lastname, email, phone, username, street, city, state, country, zip, url, uuid, company, jobtitle, productname, word, sentence, datetime, date, bool, ipv4, hexcolor, latitude, longitude, price(min,max), number(min,max), paragraph(n)
- CRITICAL: match return type to column type. If column type contains "char", "text", or "varchar" → use a string-returning function (word, sentence, uuid, url, company, etc.) NOT number() or price()
- Choose based on what the column SEMANTICALLY represents in a %s table context
- Examples: tracking_number(varchar) → uuid, brand name(varchar) → company, product name(varchar) → productname, review body(text) → paragraph(2), coupon code(varchar) → word, wishlist name(varchar) → sentence

Return only the faker function call:`, allTables, tableName, siblingCols, colName, colType, tableName)
}

func cleanFakerString(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.TrimPrefix(s, "gofakeit.")
	// Remove markdown code block formatting if present
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// Strip bare parentheses — e.g. "company()" → "company", "street()" → "street"
	// but keep parameterized calls like "price(1,1000)", "number(0,500)"
	s = strings.TrimSuffix(s, "()")
	return s
}
