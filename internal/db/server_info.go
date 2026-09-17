package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ServerInfo is what a database server says about its capacity, read-only.
type ServerInfo struct {
	Engine          string `json:"engine"`
	Version         string `json:"version"`
	MaxConnections  int    `json:"maxConnections"`
	UsedConnections int    `json:"usedConnections"`
	// Memory the server reserves for its buffers.
	SharedBuffersBytes int64 `json:"sharedBuffersBytes,omitempty"`
	BufferPoolBytes    int64 `json:"bufferPoolBytes,omitempty"`
	LogBufferBytes     int64 `json:"logBufferBytes,omitempty"`
	MaxAllowedPacket   int64 `json:"maxAllowedPacket,omitempty"`
	// UsedBytes is the current database's size on disk.
	UsedBytes int64 `json:"usedBytes"`
	// Replica is a read-only standby: Postgres in recovery, or MySQL with
	// read_only set (which a user with SUPER can still write to).
	Replica bool `json:"replica"`
	// WritesBlocked means the server refuses writes from anyone: a Postgres
	// standby, or MySQL super_read_only.
	WritesBlocked bool `json:"writesBlocked"`
	// ServerID is equal for two databases on the same server and never names
	// a database.
	ServerID string `json:"serverId"`
}

// DetectServer reads the server's capacity in a read-only transaction.
func DetectServer(ctx context.Context, conn *sql.DB, dbType string) (ServerInfo, error) {
	var info ServerInfo
	err := ReadOnce(ctx, conn, dbType, ReadLimits{}, func(ctx context.Context, q Querier) error {
		if dbType == "mysql" {
			return detectMySQL(ctx, q, &info)
		}
		return detectPostgres(ctx, q, &info)
	})
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read server capacity: %w", err)
	}
	return info, nil
}

func detectPostgres(ctx context.Context, q Querier, info *ServerInfo) error {
	info.Engine = "postgres"
	if err := q.QueryRowContext(ctx, `
		SELECT current_setting('server_version'),
		       current_setting('max_connections')::int,
		       (SELECT count(*) FROM pg_stat_activity)::int,
		       pg_size_bytes(current_setting('shared_buffers')),
		       pg_database_size(current_database()),
		       pg_is_in_recovery(),
		       pg_postmaster_start_time()::text || '/' || COALESCE(inet_server_port()::text, '')`).
		Scan(&info.Version, &info.MaxConnections, &info.UsedConnections, &info.SharedBuffersBytes, &info.UsedBytes, &info.Replica, &info.ServerID); err != nil {
		return err
	}
	// A standby refuses writes from everyone.
	info.WritesBlocked = info.Replica
	return nil
}

func detectMySQL(ctx context.Context, q Querier, info *ServerInfo) error {
	info.Engine = "mysql"
	var readOnly, superReadOnly int
	if err := q.QueryRowContext(ctx, `
		SELECT @@version, @@max_connections, @@innodb_buffer_pool_size, @@innodb_log_buffer_size,
		       @@max_allowed_packet, @@read_only, @@super_read_only, @@server_uuid`).
		Scan(&info.Version, &info.MaxConnections, &info.BufferPoolBytes, &info.LogBufferBytes, &info.MaxAllowedPacket, &readOnly, &superReadOnly, &info.ServerID); err != nil {
		return err
	}
	// read_only still lets a user with SUPER write; super_read_only stops everyone.
	info.Replica = readOnly == 1
	info.WritesBlocked = superReadOnly == 1
	var name string
	if err := q.QueryRowContext(ctx, `SHOW STATUS LIKE 'Threads_connected'`).Scan(&name, &info.UsedConnections); err != nil {
		return err
	}
	var used sql.NullInt64
	if err := q.QueryRowContext(ctx, `
		SELECT SUM(COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0))
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()`).Scan(&used); err != nil {
		return err
	}
	info.UsedBytes = used.Int64
	info.Version = strings.TrimSpace(info.Version)
	return nil
}

// ConnectionUsage returns the server's connection limit and how many are open.
func ConnectionUsage(ctx context.Context, conn *sql.DB, dbType string) (maxConns, used int, err error) {
	err = ReadOnce(ctx, conn, dbType, DefaultCountLimits, func(ctx context.Context, q Querier) error {
		if dbType == "mysql" {
			if err := q.QueryRowContext(ctx, `SELECT @@max_connections`).Scan(&maxConns); err != nil {
				return err
			}
			var name string
			return q.QueryRowContext(ctx, `SHOW STATUS LIKE 'Threads_connected'`).Scan(&name, &used)
		}
		return q.QueryRowContext(ctx, `SELECT current_setting('max_connections')::int, (SELECT count(*) FROM pg_stat_activity)::int`).Scan(&maxConns, &used)
	})
	return maxConns, used, err
}
