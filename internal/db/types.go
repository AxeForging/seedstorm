package db

// Table represents a database table with its columns.
type Table struct {
	Name    string
	Columns []Column
}

// Column represents a column in a database table.
type Column struct {
	Name       string
	Type       string
	IsNullable bool
	IsPK       bool
	FK         *ForeignKey
	EnumValues []string
}

// ForeignKey represents a foreign key reference.
type ForeignKey struct {
	TableName  string
	ColumnName string
}
