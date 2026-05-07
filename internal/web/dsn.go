package web

import (
	"errors"
	"fmt"
	"net/url"
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
