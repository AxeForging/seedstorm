package db

// Table represents a database table with its columns.
type Table struct {
	Name    string
	Columns []Column
	Indexes []Index
	Comment string
	// Partition describes a Postgres partitioned table; its partitions are
	// not listed as tables of their own.
	Partition *Partitioning
}

// Partitioning is a partitioned table's key and the bounds of its partitions.
type Partitioning struct {
	Strategy string // range | list | hash
	// Columns are the key columns; empty entries are expressions.
	Columns []string
	// Ranges are the FROM/TO bounds of range partitions, as Postgres prints
	// them (quoted literals, MINVALUE, MAXVALUE).
	Ranges []PartitionRange
	// Values are the listed values of list partitions.
	Values []string
	// Default reports a DEFAULT partition, which accepts any key.
	Default bool
}

// PartitionRange is one range partition's bounds for a single-column key.
type PartitionRange struct {
	From string
	To   string
}

// Column represents a column in a database table.
type Column struct {
	Name          string
	Type          string
	DDLType       string
	IsNullable    bool
	IsPK          bool
	FK            *ForeignKey
	EnumValues    []string
	Unique        bool     // column has a single-column UNIQUE constraint
	CheckValues   []string // values extracted from a CHECK (col IN (...)) constraint
	CheckMin      *int64   // lower bound from a CHECK (col >= N) or CHECK (col BETWEEN N AND M) constraint
	CheckMax      *int64   // upper bound from a CHECK (col <= N) constraint
	Default       string
	Generated     string
	AutoIncrement bool
	Comment       string
}

// ForeignKey represents a foreign key reference.
type ForeignKey struct {
	TableName  string
	ColumnName string
}

// Index represents a non-primary index that should be recreated after tables
// and foreign keys exist.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	// Prefixes holds a MySQL prefix length per column (0 = whole column); nil
	// when no column is prefixed.
	Prefixes []int
}
