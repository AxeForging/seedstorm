package web

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// buildDSN converts a ConnectionInfo + password into a (driverName, dsn) pair
// suitable for sql.Open. The password is URL-escaped so DSNs containing
// unusual characters parse correctly.
func buildDSN(info ConnectionInfo, password string) (driver, dsn string, err error) {
	host := info.Host
	if host == "" {
		host = "localhost"
	}
	port := info.Port

	switch strings.ToLower(info.DBType) {
	case "postgres", "postgresql", "pgx":
		if port == 0 {
			port = 5432
		}
		ssl := info.SSL
		if ssl == "" {
			ssl = "disable"
		}
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(info.User, password),
			Host:   fmt.Sprintf("%s:%d", host, port),
			Path:   info.DBName,
		}
		q := u.Query()
		q.Set("sslmode", ssl)
		u.RawQuery = q.Encode()
		return "pgx", u.String(), nil
	case "mysql":
		if port == 0 {
			port = 3306
		}
		// MySQL DSN: user:pass@tcp(host:port)/dbname?parseTime=true
		dsn := fmt.Sprintf(
			"%s:%s@tcp(%s:%d)/%s?parseTime=true&multiStatements=true",
			info.User, password, host, port, info.DBName,
		)
		return "mysql", dsn, nil
	default:
		return "", "", errors.New("unsupported database type (use postgres or mysql)")
	}
}

func buildRawDSN(dbType, raw string) (driver, dsn string, info ConnectionInfo, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", ConnectionInfo{}, errors.New("connection string is required")
	}
	switch strings.ToLower(dbType) {
	case "postgres", "postgresql", "pgx":
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", ConnectionInfo{}, fmt.Errorf("parse postgres connection string: %w", err)
		}
		if u.Scheme != "postgres" && u.Scheme != "postgresql" {
			return "", "", ConnectionInfo{}, errors.New("postgres connection string must start with postgres:// or postgresql://")
		}
		port := 5432
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
		info := ConnectionInfo{
			DBType: "postgres",
			Host:   u.Hostname(),
			Port:   port,
			DBName: strings.TrimPrefix(u.Path, "/"),
			User:   u.User.Username(),
			SSL:    u.Query().Get("sslmode"),
		}
		if info.Host == "" {
			info.Host = "localhost"
		}
		return "pgx", raw, info, nil
	case "mysql":
		info := parseMySQLDisplayInfo(raw)
		return "mysql", ensureMySQLParams(raw), info, nil
	default:
		return "", "", ConnectionInfo{}, errors.New("unsupported database type (use postgres or mysql)")
	}
}

var mysqlDSNRe = regexp.MustCompile(`^([^:]+)(?::[^@]*)?@tcp\(([^:)]+)(?::(\d+))?\)/([^?]+)`)

func parseMySQLDisplayInfo(raw string) ConnectionInfo {
	info := ConnectionInfo{DBType: "mysql", Host: "localhost", Port: 3306}
	if m := mysqlDSNRe.FindStringSubmatch(raw); len(m) == 5 {
		info.User = m[1]
		info.Host = m[2]
		if m[3] != "" {
			if n, err := strconv.Atoi(m[3]); err == nil {
				info.Port = n
			}
		}
		info.DBName = m[4]
	}
	return info
}

func ensureMySQLParams(raw string) string {
	if strings.Contains(raw, "?") {
		if !strings.Contains(raw, "multiStatements=") {
			raw += "&multiStatements=true"
		}
		if !strings.Contains(raw, "parseTime=") {
			raw += "&parseTime=true"
		}
		return raw
	}
	return raw + "?parseTime=true&multiStatements=true"
}
