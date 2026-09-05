package web

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Param is a single driver connection parameter appended to a DSN's query
// string, e.g. {allowCleartextPasswords, 1}.
type Param struct {
	Name  string `json:"name" yaml:"name"`
	Value string `json:"value" yaml:"value"`
}

// ParamIssue explains why a parameter will not behave the way the user expects.
// Level "error" means the connection will almost certainly fail as written;
// "warn" means it is valid but not a driver option, so it reaches the server
// as a session/runtime setting instead.
type ParamIssue struct {
	Name    string `json:"name"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Suggest *Param `json:"suggest,omitempty"`
}

// ParamHint is a machine-readable "fix this" derived from a driver error, so
// the UI can offer a one-click correction instead of making the user decode the
// message themselves.
type ParamHint struct {
	Add    *Param `json:"add,omitempty"`
	Remove string `json:"remove,omitempty"`
	Note   string `json:"note"`
}

var paramNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// mysqlDriverParams are the DSN keys go-sql-driver/mysql interprets itself.
// Anything else is sent to the server as "SET <name> = <value>" on connect
// (see go-sql-driver/mysql connection.go handleParams), which is useful for
// session variables and fatal for typos.
var mysqlDriverParams = map[string]bool{
	"allowAllFiles": true, "allowCleartextPasswords": true, "allowFallbackToPlaintext": true,
	"allowNativePasswords": true, "allowOldPasswords": true, "charset": true,
	"checkConnLiveness": true, "clientFoundRows": true, "collation": true,
	"columnsWithAlias": true, "compress": true, "connectionAttributes": true,
	"interpolateParams": true, "loc": true, "maxAllowedPacket": true,
	"multiStatements": true, "parseTime": true, "readTimeout": true,
	"rejectReadOnly": true, "serverPubKey": true, "strict": true,
	"timeTruncate": true, "timeout": true, "tls": true, "writeTimeout": true,
}

// postgresConnParams are the keys pgx consumes for the connection itself.
// Everything else becomes a server runtime parameter (a GUC such as
// application_name or search_path) in the startup packet.
var postgresConnParams = map[string]bool{
	"host": true, "port": true, "database": true, "dbname": true, "user": true,
	"password": true, "passfile": true, "connect_timeout": true, "sslmode": true,
	"sslkey": true, "sslcert": true, "sslrootcert": true, "sslnegotiation": true,
	"sslpassword": true, "sslsni": true, "krbspn": true, "krbsrvname": true,
	"target_session_attrs": true, "service": true, "servicefile": true,
}

// paramTranslation maps a parameter from another ecosystem (usually a JDBC or
// Connector/J connection string, which is what most "add this flag" advice on
// the internet assumes) onto its Go driver equivalent.
type paramTranslation struct {
	// suggest is the replacement parameter; a nil suggest with drop=true means
	// the parameter has no Go equivalent because the driver handles it already.
	suggest func(value string) *Param
	drop    bool
	why     string
}

func constSuggest(name, value string) func(string) *Param {
	return func(string) *Param { return &Param{Name: name, Value: value} }
}

func renameSuggest(name string) func(string) *Param {
	return func(v string) *Param { return &Param{Name: name, Value: v} }
}

var mysqlTranslations = map[string]paramTranslation{
	"allowpublickeyretrieval": {
		drop: true,
		why:  "go-sql-driver requests the server's public key automatically, so this Connector/J flag has no Go equivalent and is not needed",
	},
	"usessl": {
		suggest: func(v string) *Param {
			if isFalsey(v) {
				return &Param{Name: "tls", Value: "false"}
			}
			return &Param{Name: "tls", Value: "true"}
		},
		why: "TLS is controlled by the driver's tls parameter",
	},
	"requiressl":              {suggest: constSuggest("tls", "true"), why: "TLS is controlled by the driver's tls parameter"},
	"verifyservercertificate": {suggest: constSuggest("tls", "skip-verify"), why: "use tls=skip-verify to accept a self-signed server certificate"},
	"servertimezone":          {suggest: renameSuggest("loc"), why: "the driver names this loc"},
	"characterencoding":       {suggest: renameSuggest("charset"), why: "the driver names this charset"},
	"connecttimeout":          {suggest: renameSuggest("timeout"), why: "the driver names the dial timeout timeout"},
	"sockettimeout":           {suggest: renameSuggest("readTimeout"), why: "the driver names this readTimeout"},
	"allowmultiqueries":       {suggest: renameSuggest("multiStatements"), why: "the driver names this multiStatements"},
	"useserverprepstmts":      {suggest: constSuggest("interpolateParams", "true"), why: "set interpolateParams=true to build statements client-side instead"},
	"autoreconnect":           {drop: true, why: "database/sql reconnects through the pool, so there is no driver flag for it"},
	"useunicode":              {drop: true, why: "the driver is always UTF-8 aware; use charset if you need a specific one"},
	"sessionvariables":        {drop: true, why: "add each session variable as its own parameter instead — unknown names are sent as SET name = value"},
}

var postgresTranslations = map[string]paramTranslation{
	"ssl": {
		suggest: func(v string) *Param {
			if isFalsey(v) {
				return &Param{Name: "sslmode", Value: "disable"}
			}
			return &Param{Name: "sslmode", Value: "require"}
		},
		why: "libpq/pgx spell this sslmode",
	},
	"usessl":          {suggest: constSuggest("sslmode", "require"), why: "libpq/pgx spell this sslmode"},
	"currentschema":   {suggest: renameSuggest("search_path"), why: "Postgres names this search_path"},
	"applicationname": {suggest: renameSuggest("application_name"), why: "Postgres names this application_name (lowercase, underscored)"},
	"logintimeout":    {suggest: renameSuggest("connect_timeout"), why: "libpq/pgx name this connect_timeout"},
	"allowpublickeyretrieval": {
		drop: true,
		why:  "this is a MySQL Connector/J flag and means nothing to Postgres",
	},
}

// ParamSuggestion is an autocomplete entry offered next to the parameter rows.
type ParamSuggestion struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Help  string `json:"help"`
}

// ParamCatalog is what the UI needs to guide a user through driver parameters:
// which names the driver understands, which are worth offering, and how to
// rewrite the ones borrowed from other ecosystems.
type ParamCatalog struct {
	Driver       string                `json:"driver"`
	Known        []string              `json:"known"`
	Suggestions  []ParamSuggestion     `json:"suggestions"`
	Translations map[string]ParamIssue `json:"translations"`
	UnknownNote  string                `json:"unknownNote"`
}

// paramCatalog builds the catalog for a driver. Translations are rendered with
// an empty input value; the server re-validates on test and connect, where the
// user's actual value decides the exact suggestion.
func paramCatalog(dbType string) ParamCatalog {
	driver := normalizeDriver(dbType)
	cat := ParamCatalog{
		Driver:       driver,
		Suggestions:  paramSuggestions(driver),
		Translations: map[string]ParamIssue{},
	}
	var (
		known        map[string]bool
		translations map[string]paramTranslation
	)
	if driver == "mysql" {
		known, translations = mysqlDriverParams, mysqlTranslations
		cat.UnknownNote = "Unknown names are sent to MySQL as `SET name = value`, so session variables work and typos fail."
	} else {
		known, translations = postgresConnParams, postgresTranslations
		cat.UnknownNote = "Unknown names are sent as Postgres runtime parameters, so GUCs like search_path work and typos fail."
	}
	for name := range known {
		cat.Known = append(cat.Known, name)
	}
	sort.Strings(cat.Known)
	for name, t := range translations {
		issue := ParamIssue{Name: name, Level: "error"}
		if t.drop {
			issue.Message = fmt.Sprintf("Not supported here: %s. Remove it.", t.why)
		} else {
			issue.Suggest = t.suggest("")
			issue.Message = fmt.Sprintf("Not a %s driver option: %s.", driver, t.why)
		}
		cat.Translations[name] = issue
	}
	return cat
}

// paramSuggestions returns the parameters worth surfacing for a driver, chosen
// for the cases that actually block a seeding session: authentication plugins,
// TLS, and the FK/session switches that matter while loading data.
func paramSuggestions(dbType string) []ParamSuggestion {
	switch normalizeDriver(dbType) {
	case "mysql":
		return []ParamSuggestion{
			{"allowCleartextPasswords", "1", "Required by accounts using the cleartext auth plugin (IAM / PAM / LDAP)."},
			{"allowFallbackToPlaintext", "true", "Allow an unencrypted connection when the server has no TLS."},
			{"allowNativePasswords", "true", "Permit the mysql_native_password plugin."},
			{"tls", "skip-verify", "Encrypt but do not verify the server certificate. Use true to verify."},
			{"serverPubKey", "", "Name of a pre-registered server public key for sha256 auth."},
			{"timeout", "10s", "Dial timeout."},
			{"readTimeout", "30s", "I/O read timeout."},
			{"charset", "utf8mb4", "Connection charset."},
			{"foreign_key_checks", "0", "Session variable: skip FK checks while loading data."},
			{"interpolateParams", "true", "Build statements client-side instead of server-side prepares."},
		}
	default:
		return []ParamSuggestion{
			{"connect_timeout", "10", "Seconds to wait for the connection."},
			{"application_name", "seedstorm", "Shows up in pg_stat_activity."},
			{"search_path", "public", "Schema search path for this session."},
			{"statement_timeout", "0", "Milliseconds before a statement is cancelled (0 disables)."},
			{"sslrootcert", "", "Path to the CA certificate for sslmode=verify-full."},
			{"target_session_attrs", "read-write", "Require a primary when connecting to a pool/replica set."},
		}
	}
}

func isFalsey(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no", "off", "disable", "disabled":
		return true
	}
	return false
}

func normalizeDriver(dbType string) string {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "mysql":
		return "mysql"
	case "postgres", "postgresql", "pgx", "":
		return "postgres"
	default:
		return strings.ToLower(strings.TrimSpace(dbType))
	}
}

// normalizeParams drops blank rows and trims whitespace. Later entries win over
// earlier ones for the same name, matching how the DSN query is merged.
func normalizeParams(params []Param) []Param {
	out := make([]Param, 0, len(params))
	for _, p := range params {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		out = append(out, Param{Name: name, Value: strings.TrimSpace(p.Value)})
	}
	return out
}

// validateParamNames rejects names that would corrupt the DSN query string.
func validateParamNames(params []Param) error {
	for _, p := range params {
		if !paramNameRe.MatchString(p.Name) {
			return fmt.Errorf("invalid parameter name %q: use letters, digits, underscore, dot or dash, starting with a letter or underscore", p.Name)
		}
	}
	return nil
}

// validateParams reports parameters that the driver will not read as the user
// intends. It never blocks a connection — the driver is the final authority, and
// unknown names are legitimate for session variables and GUCs.
func validateParams(dbType string, params []Param) []ParamIssue {
	driver := normalizeDriver(dbType)
	var (
		known        map[string]bool
		translations map[string]paramTranslation
		unknownNote  string
	)
	switch driver {
	case "mysql":
		known, translations = mysqlDriverParams, mysqlTranslations
		unknownNote = "not a go-sql-driver option — it is sent to the server as `SET %s = %s`, which fails unless it is a real session variable"
	default:
		known, translations = postgresConnParams, postgresTranslations
		unknownNote = "not a pgx connection option — it is sent as the server runtime parameter %s, which fails unless Postgres knows that setting"
	}

	issues := make([]ParamIssue, 0, len(params))
	for _, p := range params {
		if known[p.Name] {
			continue
		}
		if t, ok := translations[strings.ToLower(p.Name)]; ok {
			issue := ParamIssue{Name: p.Name, Level: "error"}
			if t.drop {
				issue.Message = fmt.Sprintf("%s is not supported here: %s. Remove it.", p.Name, t.why)
			} else {
				issue.Suggest = t.suggest(p.Value)
				issue.Message = fmt.Sprintf("%s is not a %s driver option: %s. Use %s=%s instead.",
					p.Name, driver, t.why, issue.Suggest.Name, issue.Suggest.Value)
			}
			issues = append(issues, issue)
			continue
		}
		var msg string
		if driver == "mysql" {
			msg = fmt.Sprintf(unknownNote, p.Name, p.Value)
		} else {
			msg = fmt.Sprintf(unknownNote, p.Name)
		}
		issues = append(issues, ParamIssue{Name: p.Name, Level: "warn", Message: msg})
	}
	return issues
}

var (
	quotedParamRe    = regexp.MustCompile(`'([A-Za-z_][A-Za-z0-9_.-]*)=([^']*)'`)
	unknownSysVarRe  = regexp.MustCompile(`(?i)unknown system variable '([^']+)'`)
	unknownGUCRe     = regexp.MustCompile(`(?i)unrecognized configuration parameter "([^"]+)"`)
	unknownGUCAltRe  = regexp.MustCompile(`(?i)unrecognized configuration parameter '([^']+)'`)
	postgresNoSSLRe  = regexp.MustCompile(`(?i)server (refused|does not support) (tls|ssl)`)
	mysqlNoTLSErrStr = "tls requested but server does not support tls"
)

// paramHintFromError turns a driver error into a one-click correction. It
// returns nil when the message carries no actionable parameter — no guessing.
func paramHintFromError(dbType, msg string) *ParamHint {
	if strings.TrimSpace(msg) == "" {
		return nil
	}
	lower := strings.ToLower(msg)
	driver := normalizeDriver(dbType)

	if m := unknownSysVarRe.FindStringSubmatch(msg); len(m) == 2 {
		return &ParamHint{
			Remove: m[1],
			Note:   fmt.Sprintf("%q is not a driver option, so it was sent to MySQL as a session variable and rejected. Remove it.", m[1]),
		}
	}
	for _, re := range []*regexp.Regexp{unknownGUCRe, unknownGUCAltRe} {
		if m := re.FindStringSubmatch(msg); len(m) == 2 {
			return &ParamHint{
				Remove: m[1],
				Note:   fmt.Sprintf("Postgres does not know the setting %q, so it was rejected at startup. Remove it.", m[1]),
			}
		}
	}
	if m := quotedParamRe.FindStringSubmatch(msg); len(m) == 3 {
		return &ParamHint{
			Add:  &Param{Name: m[1], Value: m[2]},
			Note: fmt.Sprintf("The driver asked for %s=%s.", m[1], m[2]),
		}
	}
	if driver == "mysql" && strings.Contains(lower, mysqlNoTLSErrStr) {
		return &ParamHint{
			Add:  &Param{Name: "allowFallbackToPlaintext", Value: "true"},
			Note: "The server has no TLS. Allow the fallback, or drop the tls parameter.",
		}
	}
	if driver == "postgres" && postgresNoSSLRe.MatchString(msg) {
		return &ParamHint{
			Add:  &Param{Name: "sslmode", Value: "disable"},
			Note: "The server is not running TLS.",
		}
	}
	return nil
}
