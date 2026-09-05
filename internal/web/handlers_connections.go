package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// connectForm is everything the connect page collects, kept as strings so a
// failed attempt can be re-rendered exactly as the user typed it.
type connectForm struct {
	ID            string
	Label         string
	DBType        string
	Host          string
	Port          string
	DBName        string
	User          string
	SSL           string
	DSN           string
	Password      string
	Params        []Param
	Save          bool
	SavePassword  bool
	NeedsPassword bool
	// Origin is "edit" or "duplicate" when the form was opened from a saved
	// connection, so the page can say which it is.
	Origin string
}

// connectPage is the view model for the connect template.
type connectPage struct {
	Mode        string
	Saved       []SavedConnection
	LiveIDs     map[string]string
	Form        connectForm
	Suggestions []ParamSuggestion
	Issues      []ParamIssue
	Hint        *ParamHint
	StoreError  string
	StorePath   string
}

const paramRowLimit = 40

// maxFormMemory bounds an in-memory multipart parse. The connect form carries
// only short text fields.
const maxFormMemory = 1 << 20

// parseAnyForm reads the form body whether it arrived urlencoded (a plain HTML
// form post) or as multipart (a fetch carrying a FormData). http.ParseForm
// silently ignores a multipart body, which would leave every field empty.
func parseAnyForm(r *http.Request) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return r.ParseMultipartForm(maxFormMemory)
	}
	return r.ParseForm()
}

func parseConnectForm(r *http.Request) connectForm {
	port := strings.TrimSpace(r.FormValue("port"))
	f := connectForm{
		ID:           strings.TrimSpace(r.FormValue("id")),
		Label:        strings.TrimSpace(r.FormValue("label")),
		DBType:       strings.TrimSpace(r.FormValue("dbType")),
		Host:         strings.TrimSpace(r.FormValue("host")),
		Port:         port,
		DBName:       strings.TrimSpace(r.FormValue("dbName")),
		User:         strings.TrimSpace(r.FormValue("user")),
		SSL:          strings.TrimSpace(r.FormValue("ssl")),
		DSN:          strings.TrimSpace(r.FormValue("dsn")),
		Password:     r.FormValue("password"),
		Save:         isChecked(r.FormValue("save")),
		SavePassword: isChecked(r.FormValue("savePassword")),
	}
	names := r.Form["paramName"]
	values := r.Form["paramValue"]
	for i, name := range names {
		if i >= paramRowLimit {
			break
		}
		value := ""
		if i < len(values) {
			value = values[i]
		}
		f.Params = append(f.Params, Param{Name: name, Value: value})
	}
	f.Params = normalizeParams(f.Params)
	return f
}

func isChecked(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "off", "no":
		return false
	}
	return true
}

func (f connectForm) info() ConnectionInfo {
	port, _ := strconv.Atoi(f.Port)
	return ConnectionInfo{
		Label:  f.Label,
		DBType: f.DBType,
		Host:   f.Host,
		Port:   port,
		DBName: f.DBName,
		User:   f.User,
		SSL:    f.SSL,
		Params: f.Params,
	}
}

func (f connectForm) saved() SavedConnection {
	port, _ := strconv.Atoi(f.Port)
	c := SavedConnection{
		ID:     f.ID,
		Label:  f.Label,
		DBType: f.DBType,
		DSN:    f.DSN,
		Params: f.Params,
	}
	if f.DSN == "" {
		c.Host = f.Host
		c.Port = port
		c.DBName = f.DBName
		c.User = f.User
		c.SSL = f.SSL
	}
	if f.SavePassword {
		c.Password = f.Password
	}
	return c
}

// dsnFor builds the driver and DSN for a form, using the raw connection string
// when one was supplied and the structured fields otherwise. Both paths merge
// the same extra parameters, so the two entry modes have identical capability.
func (f connectForm) dsnFor() (driver, dsn string, info ConnectionInfo, err error) {
	if f.DSN != "" {
		driver, dsn, info, err = buildRawDSN(f.DBType, f.DSN, f.Params)
		if err != nil {
			return "", "", ConnectionInfo{}, err
		}
		info.Label = f.Label
		return driver, dsn, info, nil
	}
	info = f.info()
	driver, dsn, err = buildDSN(info, f.Password)
	if err != nil {
		return "", "", ConnectionInfo{}, err
	}
	return driver, dsn, info, nil
}

// connectPageFor assembles the connect view model, choosing the chooser when
// there is something to choose from.
func (s *Server) connectPageFor(form connectForm, mode string) connectPage {
	page := connectPage{Mode: mode, Form: form, LiveIDs: map[string]string{}}
	if s.store != nil {
		page.StorePath = s.store.DisplayPath()
		saved, err := s.store.List()
		if err != nil {
			page.StoreError = err.Error()
		}
		page.Saved = saved
	}
	for _, sess := range s.sessions.All() {
		page.LiveIDs[sessionConnectionKey(sess)] = sess.ID
	}
	if page.Form.DBType == "" {
		page.Form.DBType = "postgres"
	}
	if page.Form.Port == "" {
		if normalizeDriver(page.Form.DBType) == "mysql" {
			page.Form.Port = "3306"
		} else {
			page.Form.Port = "5432"
		}
	}
	if page.Form.Host == "" {
		page.Form.Host = "localhost"
	}
	page.Suggestions = paramSuggestions(page.Form.DBType)
	page.Issues = validateParams(page.Form.DBType, page.Form.Params)
	if page.Mode == "" {
		if len(page.Saved) > 0 {
			page.Mode = "chooser"
		} else {
			page.Mode = "form"
		}
	}
	return page
}

// LiveID reports the id of a live session already connected to this saved
// connection, so the chooser can offer "switch" instead of a second connect.
func (p connectPage) LiveID(c SavedConnection) string {
	return p.LiveIDs[savedConnectionKey(c)]
}

func (s *Server) renderConnect(w http.ResponseWriter, r *http.Request, page connectPage, errMsg string) {
	s.render(w, r, "connect", pageData{
		Title:  "Connect",
		Active: "connect",
		Error:  errMsg,
		Data:   page,
	})
}

// handleConnectTest pings a candidate connection and returns the outcome as
// JSON. It never creates a session, sets no cookie, and leaves the page as it
// is so nothing the user typed is lost.
func (s *Server) handleConnectTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := parseAnyForm(r); err != nil {
		writeError(w, http.StatusBadRequest, "bad form: "+err.Error())
		return
	}
	form := parseConnectForm(r)
	if form.ID != "" && form.Password == "" && s.store != nil {
		if saved, ok, _ := s.store.Get(form.ID); ok && saved.Password != "" {
			form.Password = saved.Password
		}
	}
	type testResult struct {
		OK        bool         `json:"ok"`
		Driver    string       `json:"driver,omitempty"`
		Target    string       `json:"target,omitempty"`
		ElapsedMs int64        `json:"elapsedMs"`
		Error     string       `json:"error,omitempty"`
		Hint      *ParamHint   `json:"hint,omitempty"`
		Issues    []ParamIssue `json:"issues,omitempty"`
	}
	issues := validateParams(form.DBType, form.Params)

	driver, dsn, info, err := form.dsnFor()
	if err != nil {
		writeJSON(w, http.StatusOK, testResult{
			OK:     false,
			Error:  err.Error(),
			Hint:   paramHintFromError(form.DBType, err.Error()),
			Issues: issues,
		})
		return
	}
	start := time.Now()
	if err := pingDSN(r.Context(), driver, dsn); err != nil {
		writeJSON(w, http.StatusOK, testResult{
			OK:        false,
			Driver:    driver,
			ElapsedMs: time.Since(start).Milliseconds(),
			Error:     err.Error(),
			Hint:      paramHintFromError(form.DBType, err.Error()),
			Issues:    issues,
		})
		return
	}
	writeJSON(w, http.StatusOK, testResult{
		OK:        true,
		Driver:    driver,
		Target:    SavedConnection{Host: info.Host, Port: info.Port, DBName: info.DBName, User: info.User}.Target(),
		ElapsedMs: time.Since(start).Milliseconds(),
		Issues:    issues,
	})
}

// pingDSN opens a connection, pings it, and closes it again. Nothing is
// retained: a test must not leave a pooled connection behind.
func pingDSN(ctx context.Context, driver, dsn string) error {
	conn, err := sqlOpen(driver, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return conn.PingContext(ctx)
}

// handleParamsCatalog serves the driver parameter catalog so the form can
// suggest, autocomplete, and flag parameters without a round trip per keystroke.
func (s *Server) handleParamsCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, paramCatalog(r.URL.Query().Get("dbType")))
}

// handleConnectSaved connects using a stored connection, asking for the
// password only when it was not saved.
func (s *Server) handleConnectSaved(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.store == nil {
		http.Error(w, "connection store unavailable", http.StatusInternalServerError)
		return
	}
	if err := parseAnyForm(r); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	saved, ok, err := s.store.Get(id)
	if err != nil {
		s.renderConnect(w, r, s.connectPageFor(connectForm{}, "chooser"), err.Error())
		return
	}
	if !ok {
		http.Error(w, "unknown connection", http.StatusNotFound)
		return
	}
	password := r.FormValue("password")
	if password == "" {
		password = saved.Password
	}
	form := savedToForm(saved, password)
	if password == "" && needsPassword(saved) {
		form.NeedsPassword = true
		page := s.connectPageFor(form, "chooser")
		s.renderConnect(w, r, page, "")
		return
	}
	driver, dsn, info, err := form.dsnFor()
	if err != nil {
		page := s.connectPageFor(form, "chooser")
		page.Form.NeedsPassword = needsPassword(saved) && password == ""
		s.renderConnect(w, r, page, err.Error())
		return
	}
	info.Label = saved.Label
	sess, err := s.sessions.OpenDSN(driver, dsn, info)
	if err != nil {
		page := s.connectPageFor(form, "chooser")
		page.Form.NeedsPassword = true
		page.Hint = paramHintFromError(saved.DBType, err.Error())
		s.renderConnect(w, r, page, err.Error())
		return
	}
	_ = s.store.Touch(saved.ID)
	setSessionCookie(w, sess.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// needsPassword reports whether connecting requires a password the store does
// not hold. A raw DSN usually embeds its own credentials.
func needsPassword(c SavedConnection) bool {
	return c.Password == "" && strings.TrimSpace(c.DSN) == ""
}

func savedToForm(c SavedConnection, password string) connectForm {
	f := connectForm{
		ID:       c.ID,
		Label:    c.Label,
		DBType:   c.DBType,
		Host:     c.Host,
		DBName:   c.DBName,
		User:     c.User,
		SSL:      c.SSL,
		DSN:      c.DSN,
		Params:   c.Params,
		Password: password,
	}
	if c.Port > 0 {
		f.Port = strconv.Itoa(c.Port)
	}
	return f
}

// handleSavedConnections is the CRUD surface for stored connections. Mutating
// calls require a header a cross-site form post cannot set.
func (s *Server) handleSavedConnections(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusInternalServerError, "connection store unavailable")
		return
	}
	if r.Method != http.MethodGet && r.Header.Get("X-Seedstorm-Request") == "" {
		writeError(w, http.StatusForbidden, "missing X-Seedstorm-Request header")
		return
	}
	switch r.Method {
	case http.MethodGet:
		conns, err := s.store.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, conns)
	case http.MethodPost, http.MethodPut:
		var body struct {
			SavedConnection
			Password      string `json:"password"`
			ClearPassword bool   `json:"clearPassword"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
			return
		}
		conn := body.SavedConnection
		conn.Password = body.Password
		if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
			conn.ID = id
		}
		if r.Method == http.MethodPut && conn.ID == "" {
			writeError(w, http.StatusBadRequest, "id is required")
			return
		}
		if err := validateParamNames(normalizeParams(conn.Params)); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := s.store.Save(conn, body.ClearPassword)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeError(w, http.StatusBadRequest, "id is required")
			return
		}
		removed, err := s.store.Delete(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !removed {
			writeError(w, http.StatusNotFound, "unknown connection")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSavedConnectionsImport migrates connections previously kept in the
// browser's localStorage into the server-side store. It is idempotent: entries
// already present are skipped.
func (s *Server) handleSavedConnectionsImport(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusInternalServerError, "connection store unavailable")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.Header.Get("X-Seedstorm-Request") == "" {
		writeError(w, http.StatusForbidden, "missing X-Seedstorm-Request header")
		return
	}
	var body struct {
		Connections []SavedConnection `json:"connections"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	added, err := s.store.Import(body.Connections)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"imported": added})
}
