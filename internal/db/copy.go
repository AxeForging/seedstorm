package db

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/AxeForging/seedstorm/internal/faultinject"
	"github.com/AxeForging/seedstorm/internal/safego"
)

// ErrCopyUnsupported reports an engine or connection without COPY support.
var ErrCopyUnsupported = errors.New("copy not supported")

// CopyRows streams rows into a Postgres table with COPY … FROM STDIN (CSV), an
// order of magnitude faster than batched INSERTs. COPY is atomic: on error no
// row of the call was written, so the caller can retry the same rows another
// way. MySQL returns ErrCopyUnsupported.
func CopyRows(ctx context.Context, conn *sql.DB, dbType, table string, rows []map[string]interface{}) error {
	if dbType != "pgx" || len(rows) == 0 {
		return ErrCopyUnsupported
	}
	cols, stmt := copyStatement(table, rows)
	c, err := conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Raw(func(driverConn any) error {
		pc, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return ErrCopyUnsupported
		}
		reader, writer := io.Pipe()
		go func() {
			// A panic while encoding rows fails this COPY instead of the process.
			writer.CloseWithError(safego.Run("copy "+table, func() error {
				if err := faultinject.Hit(ctx, "copy", table); err != nil {
					return err
				}
				return writeCSVRows(writer, cols, rows)
			}))
		}()
		_, err := pc.Conn().PgConn().CopyFrom(ctx, reader, stmt)
		_ = reader.Close()
		return err
	})
}

func copyStatement(table string, rows []map[string]interface{}) ([]string, string) {
	cols := make([]string, 0, len(rows[0]))
	for c := range rows[0] {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = QuoteIdent(c, "pgx")
	}
	return cols, fmt.Sprintf("COPY %s (%s) FROM STDIN WITH (FORMAT csv)", QuoteIdent(table, "pgx"), strings.Join(quoted, ", "))
}

// writeCSVRows renders rows for COPY CSV. NULL is an unquoted empty field and
// every value is quoted, which keeps empty strings distinct from NULL. Values
// are rendered as the text Postgres would parse for a string parameter.
func writeCSVRows(w io.Writer, cols []string, rows []map[string]interface{}) error {
	var sb strings.Builder
	for _, row := range rows {
		sb.Reset()
		for i, c := range cols {
			if i > 0 {
				sb.WriteByte(',')
			}
			v := row[c]
			if v == nil {
				continue
			}
			sb.WriteByte('"')
			sb.WriteString(strings.ReplaceAll(csvText(v), `"`, `""`))
			sb.WriteByte('"')
		}
		sb.WriteByte('\n')
		if _, err := io.WriteString(w, sb.String()); err != nil {
			return err
		}
	}
	return nil
}

func csvText(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "t"
		}
		return "f"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case time.Time:
		// UTC with an explicit offset reads the same for timestamp and
		// timestamptz columns, whatever the session time zone.
		return x.UTC().Format("2006-01-02 15:04:05.999999") + "+00"
	case []byte:
		return `\x` + hex.EncodeToString(x)
	default:
		return fmt.Sprint(x)
	}
}
