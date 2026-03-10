package db

// Table represents a database table with its columns.
type Table struct {
	Name    string
	Columns []Column
}

// Column represents a column in a database table.
type Column struct {
	Name        string
	Type        string
	IsNullable  bool
	IsPK        bool
	FK          *ForeignKey
	EnumValues  []string
	Unique      bool     // column has a single-column UNIQUE constraint
	CheckValues []string // values extracted from a CHECK (col IN (...)) constraint
	CheckMin    *int64   // lower bound from a CHECK (col >= N) or CHECK (col BETWEEN N AND M) constraint
	CheckMax    *int64   // upper bound from a CHECK (col <= N) constraint
}

// ForeignKey represents a foreign key reference.
type ForeignKey struct {
	TableName  string
	ColumnName string
}
