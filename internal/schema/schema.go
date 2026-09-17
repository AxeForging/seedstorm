package schema

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Schema is the top-level structure of a schema YAML file.
type Schema struct {
	Tables map[string]Table `yaml:"tables"`
}

// Table holds all columns for a single database table.
type Table struct {
	Columns map[string]Column `yaml:"columns"`
	// Unique lists multi-column UNIQUE constraints (single-column ones are
	// flagged on the column).
	Unique [][]string `yaml:"unique,omitempty"`
	// PartitionedBy describes a partitioned table's key, e.g. "range (created_at)".
	PartitionedBy string `yaml:"partitioned_by,omitempty"`
	// Unseedable says why rows cannot be generated for the table unless a
	// value rule sets them (e.g. "partitioned by an expression").
	Unseedable string `yaml:"unseedable,omitempty"`
}

// Column holds metadata and faker mapping for a single column.
type Column struct {
	Type      string `yaml:"type"`
	DDLType   string `yaml:"ddl_type,omitempty"`
	Faker     string `yaml:"faker,omitempty"`
	FK        string `yaml:"fk,omitempty"`
	PK        bool   `yaml:"pk,omitempty"`
	Nullable  bool   `yaml:"nullable,omitempty"`
	Unique    bool   `yaml:"unique,omitempty"`
	Generated bool   `yaml:"generated,omitempty"`
	// PartitionKey marks the key column of a partitioned table: its faker
	// keeps values inside the partitions, also when the column is in the PK.
	PartitionKey bool `yaml:"partition_key,omitempty"`
}

// Load reads a schema YAML file from disk.
func Load(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read schema file %s: %w", path, err)
	}

	var s Schema
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to parse schema file %s: %w", path, err)
	}

	return &s, nil
}

// Save writes a schema to a YAML file on disk.
func Save(path string, s *Schema) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write schema file %s: %w", path, err)
	}

	return nil
}
