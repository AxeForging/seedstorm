package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
)

const maxRetries = 3

// retryBaseDelay is the base delay for exponential backoff. Tests override this
// to avoid slow waits.
var retryBaseDelay = time.Second

// generateWithRetry wraps an LLM call with exponential backoff.
func generateWithRetry(ctx context.Context, llm llms.Model, prompt, label string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		answer, err := llms.GenerateFromSinglePrompt(ctx, llm, prompt)
		if err == nil {
			return answer, nil
		}
		lastErr = err
		if attempt < maxRetries-1 {
			delay := time.Duration(1<<uint(attempt)) * retryBaseDelay
			logging.Log.Warn().
				Str("target", label).
				Int("attempt", attempt+1).
				Dur("retry_in", delay).
				Err(err).
				Msg("AI call failed — retrying")
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	return "", fmt.Errorf("AI call failed after %d attempts for %s: %w", maxRetries, label, lastErr)
}

// EnrichFakerMappings uses Gemini to produce semantically meaningful faker mappings
// for columns that lack one or have a generic fallback.
// model is the Gemini model ID (e.g. "gemini-2.5-flash", "gemini-1.5-pro").
// appContext is an optional free-text hint describing the application domain
// (e.g. "TacoShop", "HR management system") that is injected into every prompt.
func EnrichFakerMappings(ctx context.Context, s *schema.Schema, model, appContext string) (*schema.Schema, string, error) {
	log := logging.Log
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
		// Collect enrichable columns for this table
		var toEnrich []enrichCol
		for colName, col := range table.Columns {
			if col.PK || col.FK != "" {
				continue
			}
			if !shouldEnrich(col.Faker, appContext) {
				continue
			}
			toEnrich = append(toEnrich, enrichCol{name: colName, colType: col.Type})
		}
		if len(toEnrich) == 0 {
			continue
		}

		// Build column context
		colNames := make([]string, 0, len(table.Columns))
		for colName := range table.Columns {
			colNames = append(colNames, colName)
		}
		colContext := strings.Join(colNames, ", ")

		// Single column → use the original per-column prompt (simpler, no JSON parsing)
		if len(toEnrich) == 1 {
			col := toEnrich[0]
			prompt := buildPrompt(tableName, col.name, col.colType, colContext, tableContext, appContext)
			answer, err := generateWithRetry(ctx, llm, prompt, tableName+"."+col.name)
			if err != nil {
				return nil, "", err
			}
			cleaned := cleanFakerString(answer)
			if !faker.ValidFaker(cleaned) {
				log.Warn().
					Str("table", tableName).
					Str("column", col.name).
					Str("ai_response", cleaned).
					Msg("AI returned unrecognised faker — keeping original")
				continue
			}

			c := s.Tables[tableName].Columns[col.name]
			c.Faker = cleaned
			tbl := s.Tables[tableName]
			tbl.Columns[col.name] = c
			s.Tables[tableName] = tbl
			continue
		}

		// Multiple columns → batch into a single prompt returning JSON
		prompt := buildBatchPrompt(tableName, toEnrich, colContext, tableContext, appContext)
		answer, err := generateWithRetry(ctx, llm, prompt, "batch:"+tableName)
		if err != nil {
			return nil, "", err
		}

		mappings, err := parseBatchResponse(answer)
		if err != nil {
			log.Warn().Str("table", tableName).Err(err).Msg("Failed to parse batch AI response — skipping table")
			continue
		}

		tbl := s.Tables[tableName]
		for _, col := range toEnrich {
			if fakerVal, ok := mappings[col.name]; ok {
				c := tbl.Columns[col.name]
				c.Faker = cleanFakerString(fakerVal)
				tbl.Columns[col.name] = c
			}
		}
		s.Tables[tableName] = tbl
	}

	return s, model, nil
}

// enrichCol describes a column that should be sent to the AI for faker mapping.
type enrichCol struct {
	name    string
	colType string
}

// buildBatchPrompt constructs a prompt that asks the AI for faker mappings for
// multiple columns at once, returning a JSON object.
func buildBatchPrompt(tableName string, cols []enrichCol, siblingCols, allTables, appContext string) string {
	// Build column list for the prompt
	var colLines []string
	for _, col := range cols {
		colLines = append(colLines, fmt.Sprintf("  - %s (type: %s)", col.name, col.colType))
	}

	domainLine := ""
	if appContext != "" {
		domainLine = fmt.Sprintf("- Application domain / context: %s\n", appContext)
	}

	domainRules := ""
	if appContext != "" {
		domainRules = `
DOMAIN-AWARE RULES:
- Infer what each column represents from its name and the application domain.
- For NUMERIC columns: return realistic number(min,max) or price(min,max) ranges.
- For STRING columns (char/text/varchar): return randomstring(val1,val2,...) with 20-30 domain-specific values for name/label columns, or paragraph(2) for long text.
- Do NOT use randomstring for personal data (emails, names, phones) or UUIDs.`
	}

	return fmt.Sprintf(`You are a database seeding expert. Given multiple database columns, return the most appropriate gofakeit faker for each.

Database context:
- All tables: %s
- Current table: %s
- Columns in this table: %s
%s
Columns to map:
%s

Rules:
- Use lowercase, no "gofakeit." prefix
- Valid functions: name, firstname, lastname, email, phone, username, street, city, state, country, zip, url, uuid, company, jobtitle, productname, word, sentence, datetime, date, bool, ipv4, hexcolor, latitude, longitude, price(min,max), number(min,max), paragraph(n), randomstring(val1,val2,...)
- CRITICAL: match return type to column type. If column type contains "char", "text", or "varchar" → use a string function, NOT number() or price()
%s
Return ONLY a JSON object mapping column name to faker. Example:
{"column_name": "email", "other_column": "number(1,100)"}`,
		allTables, tableName, siblingCols, domainLine,
		strings.Join(colLines, "\n"), domainRules)
}

// parseBatchResponse extracts a column→faker JSON mapping from the AI response.
func parseBatchResponse(answer string) (map[string]string, error) {
	s := strings.TrimSpace(answer)
	// Strip markdown code fences if present
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	var result map[string]string
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil, fmt.Errorf("invalid JSON in AI response: %w", err)
	}
	return result, nil
}

// shouldEnrich returns true if a column's faker mapping should be sent to the AI.
// Without appContext only truly generic fallbacks are re-enriched.
// With appContext we also revisit wide default numeric ranges.
// paragraph() fakers are intentionally excluded: they already produce good
// free-text output and AI tends to replace them with large randomstring() pools
// which causes the enum top-up logic to generate far more rows than requested.
func shouldEnrich(faker, appContext string) bool {
	if faker == "" || faker == "word" || faker == "sentence" {
		return true
	}
	if appContext == "" {
		return false
	}
	// With domain context, revisit wide-range numeric defaults but NOT paragraph:
	return faker == "number(1,10000)" ||
		faker == "price(1,1000)"
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

	domainRules := ""
	if appContext != "" {
		stringSection := ""
		if isStringType(colType) {
			stringSection = `
STRING DOMAIN VALUES (when appContext is set and column is a string type):
- For NAME / LABEL columns (entity names, categories, tags, options, etc.):
  Return randomstring with 20-30 distinct realistic values inferred from the column name and domain.
  No quotes around values. Comma-separated. More values = more seed variety.
- For SHORT DESCRIPTION / NOTES / SUMMARY columns:
  Return randomstring with 15-20 short natural-language sentences inferred from the column name and domain.
  No quotes. Comma-separated.
- For LONG BODY text (reviews, blog posts, articles): use paragraph(2).
- Do NOT use randomstring for personal data (emails, names, phones) or UUIDs — use gofakeit functions.`
		}
		domainRules = fmt.Sprintf(`
DOMAIN-AWARE RULES (appContext is set — apply to all column types):
- Infer what this column represents from its name and the application domain context.
- For NUMERIC columns (integer, decimal, numeric): return a realistic number(min,max) or price(min,max)
  range that makes real-world sense for what this column stores in this domain.
  Do NOT use wide defaults like number(1,10000) — think about actual real-world values.
%s`, stringSection)
	}

	returnLine := "Return ONLY the gofakeit function name or call, nothing else"
	if appContext != "" {
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
%s
Return only the faker:`, allTables, tableName, siblingCols, colName, colType, domainLine, returnLine, tableName, domainRules)
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
