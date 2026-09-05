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
// suitable for sql.Open. The password is URL-escaped for Postgres so DSNs
// containing unusual characters parse correctly; the MySQL DSN format takes the
// password verbatim, which is what its parser expects. Extra parameters carried
// on the ConnectionInfo are merged into the query string and win over the
// defaults seedstorm sets.
func buildDSN(info ConnectionInfo, password string) (driver, dsn string, err error) {
	host := info.Host
	if host == "" {
		host = "localhost"
	}
	port := info.Port
	extras := normalizeParams(info.Params)
	if err := validateParamNames(extras); err != nil {
		return "", "", err
	}

	switch normalizeDriver(info.DBType) {
	case "postgres":
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
		for _, p := range extras {
			q.Set(p.Name, p.Value)
		}
		u.RawQuery = q.Encode()
		return "pgx", u.String(), nil
	case "mysql":
		if port == 0 {
			port = 3306
		}
		// MySQL DSN: user:pass@tcp(host:port)/dbname?parseTime=true
		base := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", info.User, password, host, port, info.DBName)
		return "mysql", base + mysqlQuerySuffix(nil, extras), nil
	default:
		return "", "", errors.New("unsupported database type (use postgres or mysql)")
	}
}

func buildRawDSN(dbType, raw string, extras []Param) (driver, dsn string, info ConnectionInfo, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", ConnectionInfo{}, errors.New("connection string is required")
	}
	extras = normalizeParams(extras)
	if err := validateParamNames(extras); err != nil {
		return "", "", ConnectionInfo{}, err
	}
	switch normalizeDriver(dbType) {
	case "postgres":
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
		q := u.Query()
		for _, p := range extras {
			q.Set(p.Name, p.Value)
		}
		u.RawQuery = q.Encode()
		info := ConnectionInfo{
			DBType: "postgres",
			Host:   u.Hostname(),
			Port:   port,
			DBName: strings.TrimPrefix(u.Path, "/"),
			User:   u.User.Username(),
			SSL:    q.Get("sslmode"),
			Params: extras,
		}
		if info.Host == "" {
			info.Host = "localhost"
		}
		return "pgx", u.String(), info, nil
	case "mysql":
		base, query := splitMySQLDSN(raw)
		info := parseMySQLDisplayInfo(raw)
		info.Params = extras
		return "mysql", base + mysqlQuerySuffix(parseMySQLQuery(query), extras), info, nil
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

// splitMySQLDSN separates a MySQL DSN into its base and its query string using
// the same rule go-sql-driver applies: scan for the last '/', then the first
// '?' after it. Splitting on the last '?' would misfire on a password
// containing one.
func splitMySQLDSN(raw string) (base, query string) {
	slash := strings.LastIndex(raw, "/")
	if slash < 0 {
		return raw, ""
	}
	if q := strings.Index(raw[slash:], "?"); q >= 0 {
		return raw[:slash+q], raw[slash+q+1:]
	}
	return raw, ""
}

// parseMySQLQuery splits a MySQL DSN query string into ordered pairs, keeping
// values exactly as written. The driver decodes some parameters and reads
// others verbatim, so re-encoding what the user typed would corrupt values such
// as charset=utf8mb4,utf8.
func parseMySQLQuery(query string) []Param {
	if query == "" {
		return nil
	}
	parts := strings.Split(query, "&")
	out := make([]Param, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		if name == "" {
			continue
		}
		out = append(out, Param{Name: name, Value: value})
	}
	return out
}

// mysqlQuerySuffix merges the parameters already present in a DSN with
// seedstorm's defaults and the user's extras, and renders them back as a query
// string (including the leading '?'). Extras override existing keys; defaults
// only fill keys nobody set.
func mysqlQuerySuffix(existing, extras []Param) string {
	merged := make([]Param, 0, len(existing)+len(extras)+2)
	index := make(map[string]int, len(existing)+len(extras)+2)
	set := func(name, value string) {
		if i, ok := index[name]; ok {
			merged[i].Value = value
			return
		}
		index[name] = len(merged)
		merged = append(merged, Param{Name: name, Value: value})
	}

	for _, p := range existing {
		set(p.Name, p.Value)
	}
	for _, p := range extras {
		set(p.Name, escapeMySQLValue(p.Value))
	}
	for _, d := range []Param{{"parseTime", "true"}, {"multiStatements", "true"}} {
		if _, ok := index[d.Name]; !ok {
			set(d.Name, d.Value)
		}
	}
	if len(merged) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(merged))
	for _, p := range merged {
		pairs = append(pairs, p.Name+"="+p.Value)
	}
	return "?" + strings.Join(pairs, "&")
}

// escapeMySQLValue encodes only what would break DSN parsing. Percent-encoding
// the whole value would corrupt parameters the driver reads literally, such as
// charset or tls.
func escapeMySQLValue(v string) string {
	return strings.ReplaceAll(v, "&", "%26")
}
