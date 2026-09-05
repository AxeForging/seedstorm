package web

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// ── fake database driver ───────────────────────────────────────────────────

type fakeConn struct{}

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not implemented") }

type fakeConnector struct {
	mu       sync.Mutex
	err      error
	closed   bool
	connects int
}

func (c *fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c.mu.Lock()
	c.connects++
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return fakeConn{}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return nil }

// Close is called by sql.DB.Close, which is how the test proves a connection
// test does not leak a pooled connection.
func (c *fakeConnector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *fakeConnector) wasClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

type sqlOpenCall struct {
	driver string
	dsn    string
}

// stubSQL replaces the package's sql.Open indirection and records every DSN it
// is asked to open.
func stubSQL(t *testing.T, connErr error) (*[]sqlOpenCall, *fakeConnector) {
	t.Helper()
	calls := &[]sqlOpenCall{}
	connector := &fakeConnector{err: connErr}
	prev := sqlOpen
	sqlOpen = func(driverName, dsn string) (*sql.DB, error) {
		*calls = append(*calls, sqlOpenCall{driver: driverName, dsn: dsn})
		return sql.OpenDB(connector), nil
	}
	t.Cleanup(func() { sqlOpen = prev })
	return calls, connector
}

func newConnectTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(testOptions(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

// noRedirect keeps 303s visible to the test instead of following them.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func postForm(t *testing.T, srv *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()
	res, err := noRedirectClient().PostForm(srv.URL+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func bodyOf(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func decodeJSON[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return out
}

type testResponse struct {
	OK        bool         `json:"ok"`
	Driver    string       `json:"driver"`
	Target    string       `json:"target"`
	ElapsedMs int64        `json:"elapsedMs"`
	Error     string       `json:"error"`
	Hint      *ParamHint   `json:"hint"`
	Issues    []ParamIssue `json:"issues"`
}

func connectFormValues() url.Values {
	return url.Values{
		"dbType": {"mysql"},
		"host":   {"127.0.0.1"},
		"port":   {"3306"},
		"dbName": {"app"},
		"user":   {"seedstorm"},
		"ssl":    {"disable"},
	}
}

// ── /connect/test ──────────────────────────────────────────────────────────

func TestConnectTest_successCreatesNoSession(t *testing.T) {
	s, srv := newConnectTestServer(t)
	_, connector := stubSQL(t, nil)

	res := postForm(t, srv, "/connect/test", connectFormValues())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	got := decodeJSON[testResponse](t, res)
	if !got.OK {
		t.Fatalf("test failed: %+v", got)
	}
	if got.Driver != "mysql" {
		t.Fatalf("driver = %q", got.Driver)
	}
	if got.Target != "seedstorm@127.0.0.1:3306/app" {
		t.Fatalf("target = %q", got.Target)
	}
	if len(s.sessions.All()) != 0 {
		t.Fatal("a connection test must not register a session")
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			t.Fatal("a connection test must not set the session cookie")
		}
	}
	if !connector.wasClosed() {
		t.Fatal("the test connection was not closed")
	}
}

func TestConnectTest_failureReportsDriverErrorAndHint(t *testing.T) {
	_, srv := newConnectTestServer(t)
	driverErr := errors.New("this user requires clear text authentication. If you still want to use it, please add 'allowCleartextPasswords=1' to your DSN")
	_, connector := stubSQL(t, driverErr)

	res := postForm(t, srv, "/connect/test", connectFormValues())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: the transport succeeded even though the database refused", res.StatusCode)
	}
	got := decodeJSON[testResponse](t, res)
	if got.OK {
		t.Fatal("expected a failed test")
	}
	if !strings.Contains(got.Error, "clear text authentication") {
		t.Fatalf("driver error not passed through verbatim: %q", got.Error)
	}
	if got.Hint == nil || got.Hint.Add == nil || got.Hint.Add.Name != "allowCleartextPasswords" {
		t.Fatalf("hint = %+v, want an allowCleartextPasswords suggestion", got.Hint)
	}
	if !connector.wasClosed() {
		t.Fatal("a failed test must still close its connection")
	}
}

func TestConnectTest_flagsUnsupportedParamsBeforeDialing(t *testing.T) {
	_, srv := newConnectTestServer(t)
	stubSQL(t, nil)

	form := connectFormValues()
	form.Set("paramName", "allowPublicKeyRetrieval")
	form.Set("paramValue", "true")
	res := postForm(t, srv, "/connect/test", form)
	got := decodeJSON[testResponse](t, res)
	if len(got.Issues) != 1 || got.Issues[0].Level != "error" {
		t.Fatalf("issues = %+v, want one error about a JDBC-only parameter", got.Issues)
	}
	if !strings.Contains(got.Issues[0].Message, "Remove it") {
		t.Fatalf("message should tell the user what to do: %q", got.Issues[0].Message)
	}
}

// A passing test must imply a working connect, so both paths have to build
// byte-identical DSNs from the same input.
func TestConnectTest_buildsTheSameDSNAsConnect(t *testing.T) {
	_, srv := newConnectTestServer(t)
	calls, _ := stubSQL(t, nil)

	form := connectFormValues()
	form.Set("password", "pw")
	form["paramName"] = []string{"allowCleartextPasswords", "tls"}
	form["paramValue"] = []string{"1", "skip-verify"}

	postForm(t, srv, "/connect/test", form)
	postForm(t, srv, "/connect", form)

	if len(*calls) != 2 {
		t.Fatalf("expected one open per path, got %d", len(*calls))
	}
	test, connect := (*calls)[0], (*calls)[1]
	if test.driver != connect.driver || test.dsn != connect.dsn {
		t.Fatalf("test and connect disagree:\n test:    %s %s\n connect: %s %s",
			test.driver, test.dsn, connect.driver, connect.dsn)
	}
	if !strings.Contains(test.dsn, "allowCleartextPasswords=1") {
		t.Fatalf("extra params missing from the dsn: %q", test.dsn)
	}
}

func TestConnectTest_rawDSNAndFieldsBothAcceptParams(t *testing.T) {
	_, srv := newConnectTestServer(t)
	calls, _ := stubSQL(t, nil)

	form := url.Values{
		"dbType":     {"mysql"},
		"dsn":        {"u:p@tcp(h:3306)/db"},
		"paramName":  {"allowCleartextPasswords"},
		"paramValue": {"1"},
	}
	postForm(t, srv, "/connect/test", form)
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v", *calls)
	}
	if !strings.Contains((*calls)[0].dsn, "allowCleartextPasswords=1") {
		t.Fatalf("raw DSN path ignored the extra parameter: %q", (*calls)[0].dsn)
	}
}

func TestPingDSN_respectsContextCancellation(t *testing.T) {
	stubSQL(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pingDSN(ctx, "mysql", "u:p@tcp(h:3306)/db"); err == nil {
		t.Fatal("expected a cancelled ping to fail")
	}
}

// ── connect page modes ─────────────────────────────────────────────────────

func TestConnectPage_showsFormWhenNothingIsSaved(t *testing.T) {
	_, srv := newConnectTestServer(t)
	res, err := http.Get(srv.URL + "/connect")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	body := bodyOf(t, res)
	if !strings.Contains(body, `id="connect-form"`) {
		t.Fatal("empty store should land straight on the form")
	}
	if strings.Contains(body, "Pick a connection") {
		t.Fatal("chooser should not render with nothing to choose")
	}
}

func TestConnectPage_showsChooserWhenConnectionsExist(t *testing.T) {
	s, srv := newConnectTestServer(t)
	if _, err := s.store.Save(SavedConnection{
		Label: "orders db", DBType: "postgres", Host: "db.internal", Port: 5432,
		DBName: "orders", User: "seedstorm", Password: "pw",
	}, false); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	res, err := http.Get(srv.URL + "/connect")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	body := bodyOf(t, res)
	for _, want := range []string{
		"Pick a connection",
		"orders db",
		"seedstorm@db.internal:5432/orders",
		"saved password",
		"Add connection",
		"Duplicate",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("chooser missing %q", want)
		}
	}
	if strings.Contains(body, "pw") && strings.Contains(body, `value="pw"`) {
		t.Fatal("the chooser must not render the stored password")
	}

	// The form is still one click away.
	formRes, err := http.Get(srv.URL + "/connect?mode=form")
	if err != nil {
		t.Fatalf("GET form: %v", err)
	}
	defer formRes.Body.Close()
	if !strings.Contains(bodyOf(t, formRes), `id="connect-form"`) {
		t.Fatal("mode=form should render the form")
	}
}

func TestConnectPage_savedConnectionWithoutPasswordOffersAPrompt(t *testing.T) {
	s, srv := newConnectTestServer(t)
	saved, _ := s.store.Save(SavedConnection{
		Label: "no secret", DBType: "postgres", Host: "h", Port: 5432, DBName: "app", User: "u",
	}, false)

	res, err := http.Get(srv.URL + "/connect")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	body := bodyOf(t, res)
	if !strings.Contains(body, `id="pw-`+saved.ID+`"`) {
		t.Fatal("a connection without a stored password should offer an inline password field")
	}
	if strings.Contains(body, "needs secret") || strings.Contains(body, "disabled") {
		t.Fatal("a connection without a stored password must not be rendered as unusable")
	}
}

func TestConnectPage_editPrefillsEveryField(t *testing.T) {
	s, srv := newConnectTestServer(t)
	saved, _ := s.store.Save(SavedConnection{
		Label: "staging", DBType: "mysql", Host: "db.internal", Port: 3307,
		DBName: "app", User: "seedstorm",
		Params: []Param{{Name: "allowCleartextPasswords", Value: "1"}},
	}, false)

	res, err := http.Get(srv.URL + "/connect?edit=" + saved.ID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	body := bodyOf(t, res)
	for _, want := range []string{
		"Edit connection",
		`value="staging"`,
		`value="db.internal"`,
		`value="3307"`,
		`value="allowCleartextPasswords"`,
		`value="` + saved.ID + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("edit form missing %q", want)
		}
	}
}

func TestConnectPage_duplicateCopiesSettingsWithoutIdentity(t *testing.T) {
	s, srv := newConnectTestServer(t)
	saved, _ := s.store.Save(SavedConnection{
		Label: "staging", DBType: "mysql", Host: "db.internal", Port: 3307,
		DBName: "app", User: "seedstorm", Password: "topsecret",
		Params: []Param{{Name: "tls", Value: "skip-verify"}},
	}, false)

	res, err := http.Get(srv.URL + "/connect?duplicate=" + saved.ID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	body := bodyOf(t, res)
	if !strings.Contains(body, "Duplicate connection") {
		t.Fatal("duplicate should say what it is doing")
	}
	if !strings.Contains(body, `value="staging copy"`) {
		t.Fatal("duplicate should suggest a distinct name")
	}
	if !strings.Contains(body, `value="db.internal"`) || !strings.Contains(body, `value="skip-verify"`) {
		t.Fatal("duplicate should carry the settings over")
	}
	if strings.Contains(body, `value="`+saved.ID+`"`) {
		t.Fatal("duplicate must not reuse the original's id, or saving would overwrite it")
	}
	if strings.Contains(body, "topsecret") {
		t.Fatal("duplicate must not copy the password into the page")
	}
}

// ── connecting and saving ──────────────────────────────────────────────────

func TestConnect_savesConnectionAndOmitsPasswordUnlessAsked(t *testing.T) {
	s, srv := newConnectTestServer(t)
	stubSQL(t, nil)

	form := connectFormValues()
	form.Set("label", "local mysql")
	form.Set("password", "hunter2")
	form.Set("save", "1")
	form["paramName"] = []string{"allowCleartextPasswords"}
	form["paramValue"] = []string{"1"}

	res := postForm(t, srv, "/connect", form)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: %s", res.StatusCode, bodyOf(t, res))
	}
	conns, err := s.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("connections = %+v, want 1", conns)
	}
	if conns[0].Label != "local mysql" || conns[0].HasPassword {
		t.Fatalf("saved = %+v, want no stored password", conns[0])
	}
	if len(conns[0].Params) != 1 || conns[0].Params[0].Name != "allowCleartextPasswords" {
		t.Fatalf("params were not saved: %+v", conns[0].Params)
	}

	// Same again, this time opting in.
	form.Set("label", "local mysql with secret")
	form.Set("savePassword", "1")
	postForm(t, srv, "/connect", form)
	conns, _ = s.store.List()
	var withSecret *SavedConnection
	for i := range conns {
		if conns[i].Label == "local mysql with secret" {
			withSecret = &conns[i]
		}
	}
	if withSecret == nil || !withSecret.HasPassword {
		t.Fatalf("opting in should store the password: %+v", conns)
	}
}

func TestConnect_doesNotSaveUnlessAsked(t *testing.T) {
	s, srv := newConnectTestServer(t)
	stubSQL(t, nil)

	postForm(t, srv, "/connect", connectFormValues())
	conns, _ := s.store.List()
	if len(conns) != 0 {
		t.Fatalf("connecting without ticking remember saved %+v", conns)
	}
}

func TestConnect_failureKeepsWhatWasTyped(t *testing.T) {
	_, srv := newConnectTestServer(t)
	stubSQL(t, errors.New("Error 1045 (28000): Access denied for user 'seedstorm'@'localhost' (using password: YES)"))

	form := connectFormValues()
	form.Set("host", "db.internal")
	form.Set("label", "staging")
	form["paramName"] = []string{"allowCleartextPasswords"}
	form["paramValue"] = []string{"1"}

	res := postForm(t, srv, "/connect", form)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the form re-rendered", res.StatusCode)
	}
	body := bodyOf(t, res)
	if !strings.Contains(body, "Access denied") {
		t.Fatal("the driver error should be shown")
	}
	for _, want := range []string{`value="db.internal"`, `value="staging"`, `value="allowCleartextPasswords"`, `value="app"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("failed connect lost %q from the form", want)
		}
	}
}

func TestConnectSaved_usesStoredPassword(t *testing.T) {
	s, srv := newConnectTestServer(t)
	calls, _ := stubSQL(t, nil)
	saved, _ := s.store.Save(SavedConnection{
		Label: "with secret", DBType: "postgres", Host: "h", Port: 5432,
		DBName: "app", User: "u", Password: "hunter2",
	}, false)

	res := postForm(t, srv, "/connect/saved", url.Values{"id": {saved.ID}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: %s", res.StatusCode, bodyOf(t, res))
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0].dsn, "hunter2") {
		t.Fatalf("stored password was not used: %+v", *calls)
	}
	if len(s.sessions.All()) != 1 {
		t.Fatalf("expected one live session, got %d", len(s.sessions.All()))
	}
}

func TestConnectSaved_promptsWhenPasswordIsMissing(t *testing.T) {
	s, srv := newConnectTestServer(t)
	calls, _ := stubSQL(t, nil)
	saved, _ := s.store.Save(SavedConnection{
		Label: "no secret", DBType: "postgres", Host: "h", Port: 5432, DBName: "app", User: "u",
	}, false)

	res := postForm(t, srv, "/connect/saved", url.Values{"id": {saved.ID}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the chooser with a prompt", res.StatusCode)
	}
	if len(*calls) != 0 {
		t.Fatal("no connection should be attempted without a password")
	}
	if len(s.sessions.All()) != 0 {
		t.Fatal("no session should exist yet")
	}

	res = postForm(t, srv, "/connect/saved", url.Values{"id": {saved.ID}, "password": {"typed"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 once the password is supplied", res.StatusCode)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0].dsn, "typed") {
		t.Fatalf("typed password not used: %+v", *calls)
	}
}

func TestConnectSaved_unknownID(t *testing.T) {
	_, srv := newConnectTestServer(t)
	res := postForm(t, srv, "/connect/saved", url.Values{"id": {"c_nope"}})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestConnectSaved_touchUpdatesRecency(t *testing.T) {
	s, srv := newConnectTestServer(t)
	stubSQL(t, nil)
	first, _ := s.store.Save(SavedConnection{Label: "first", DBType: "postgres", Host: "h", Port: 5432, DBName: "a", User: "u", Password: "p"}, false)
	s.store.Save(SavedConnection{Label: "second", DBType: "postgres", Host: "h", Port: 5432, DBName: "b", User: "u", Password: "p"}, false)

	postForm(t, srv, "/connect/saved", url.Values{"id": {first.ID}})
	conns, _ := s.store.List()
	if conns[0].Label != "first" {
		t.Fatalf("the connection just used should sort first, got %q", conns[0].Label)
	}
}

// ── saved connection API ───────────────────────────────────────────────────

func apiRequest(t *testing.T, srv *httptest.Server, method, path, body string, withHeader bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if withHeader {
		req.Header.Set("X-Seedstorm-Request", "1")
	}
	res, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestSavedConnectionsAPI_crud(t *testing.T) {
	s, srv := newConnectTestServer(t)

	create := `{"label":"api made","dbType":"mysql","host":"h","port":3306,"dbName":"app","user":"u","password":"pw","params":[{"name":"tls","value":"skip-verify"}]}`
	res := apiRequest(t, srv, http.MethodPost, "/api/saved-connections", create, true)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d: %s", res.StatusCode, bodyOf(t, res))
	}
	created := decodeJSON[SavedConnection](t, res)
	if created.ID == "" || !created.HasPassword {
		t.Fatalf("created = %+v", created)
	}
	if created.Password != "" {
		t.Fatalf("API response leaked the password: %+v", created)
	}

	res = apiRequest(t, srv, http.MethodGet, "/api/saved-connections", "", false)
	list := decodeJSON[[]SavedConnection](t, res)
	if len(list) != 1 || list[0].Label != "api made" {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(bodyOf(t, apiRequest(t, srv, http.MethodGet, "/api/saved-connections", "", false)), "pw") {
		t.Fatal("list response contains the stored password")
	}

	update := `{"label":"api renamed","dbType":"mysql","host":"h","port":3306,"dbName":"app2","user":"u"}`
	res = apiRequest(t, srv, http.MethodPut, "/api/saved-connections?id="+created.ID, update, true)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d: %s", res.StatusCode, bodyOf(t, res))
	}
	got, _, _ := s.store.Get(created.ID)
	if got.Label != "api renamed" || got.DBName != "app2" {
		t.Fatalf("update did not apply: %+v", got)
	}
	if got.Password != "pw" {
		t.Fatal("an update that omits the password should keep the stored one")
	}

	res = apiRequest(t, srv, http.MethodDelete, "/api/saved-connections?id="+created.ID, "", true)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", res.StatusCode)
	}
	if list, _ := s.store.List(); len(list) != 0 {
		t.Fatalf("delete left %+v", list)
	}
	res = apiRequest(t, srv, http.MethodDelete, "/api/saved-connections?id=c_missing", "", true)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("deleting an unknown id = %d, want 404", res.StatusCode)
	}
}

func TestSavedConnectionsAPI_mutationsRequireTheRequestHeader(t *testing.T) {
	_, srv := newConnectTestServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		res := apiRequest(t, srv, method, "/api/saved-connections?id=x", `{"label":"x","dbType":"postgres"}`, false)
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("%s without the header = %d, want 403", method, res.StatusCode)
		}
	}
	// Reading is safe without it.
	if res := apiRequest(t, srv, http.MethodGet, "/api/saved-connections", "", false); res.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", res.StatusCode)
	}
}

func TestSavedConnectionsAPI_rejectsBadParamNames(t *testing.T) {
	_, srv := newConnectTestServer(t)
	body := `{"label":"bad","dbType":"mysql","host":"h","port":3306,"dbName":"a","user":"u","params":[{"name":"tls=1&x","value":"y"}]}`
	res := apiRequest(t, srv, http.MethodPost, "/api/saved-connections", body, true)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

func TestSavedConnectionsAPI_importIsIdempotent(t *testing.T) {
	s, srv := newConnectTestServer(t)
	body := `{"connections":[{"label":"legacy","dbType":"postgres","host":"localhost","port":5432,"dbName":"app","user":"u","password":"pw"}]}`

	res := apiRequest(t, srv, http.MethodPost, "/api/saved-connections/import", body, true)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d: %s", res.StatusCode, bodyOf(t, res))
	}
	if got := decodeJSON[map[string]int](t, res)["imported"]; got != 1 {
		t.Fatalf("imported = %d, want 1", got)
	}
	res = apiRequest(t, srv, http.MethodPost, "/api/saved-connections/import", body, true)
	if got := decodeJSON[map[string]int](t, res)["imported"]; got != 0 {
		t.Fatalf("re-import added %d entries", got)
	}
	if list, _ := s.store.List(); len(list) != 1 {
		t.Fatalf("store holds %+v", list)
	}
}

func TestParamsCatalogAPI(t *testing.T) {
	_, srv := newConnectTestServer(t)
	res, err := http.Get(srv.URL + "/api/params?dbType=mysql")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	cat := decodeJSON[ParamCatalog](t, res)
	if cat.Driver != "mysql" || len(cat.Known) == 0 || len(cat.Suggestions) == 0 {
		t.Fatalf("catalog = %+v", cat)
	}
	if _, ok := cat.Translations["allowpublickeyretrieval"]; !ok {
		t.Fatal("catalog should let the form flag allowPublicKeyRetrieval as you type")
	}
}

// A browser posting a FormData sends multipart, which http.ParseForm ignores.
// Without multipart handling every field arrives empty and the test dials a
// default localhost DSN instead of the one on screen.
func TestConnectTest_acceptsMultipartFormBody(t *testing.T) {
	_, srv := newConnectTestServer(t)
	calls, _ := stubSQL(t, nil)

	var body strings.Builder
	w := multipart.NewWriter(&body)
	fields := map[string]string{
		"dbType": "mysql", "host": "db.internal", "port": "3307",
		"dbName": "app", "user": "seedstorm", "password": "pw",
		"paramName": "allowCleartextPasswords", "paramValue": "1",
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/connect/test", strings.NewReader(body.String()))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()

	got := decodeJSON[testResponse](t, res)
	if !got.OK {
		t.Fatalf("test failed: %+v", got)
	}
	if got.Target != "seedstorm@db.internal:3307/app" {
		t.Fatalf("target = %q — the multipart fields were not read", got.Target)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0].dsn, "db.internal:3307") ||
		!strings.Contains((*calls)[0].dsn, "allowCleartextPasswords=1") {
		t.Fatalf("dsn built from an empty form: %+v", *calls)
	}
}

func TestConnect_editUpdatesInPlaceRatherThanDuplicating(t *testing.T) {
	s, srv := newConnectTestServer(t)
	stubSQL(t, nil)
	saved, _ := s.store.Save(SavedConnection{
		Label: "before", DBType: "postgres", Host: "h", Port: 5432,
		DBName: "app", User: "u", Password: "pw",
		Params: []Param{{Name: "application_name", Value: "old"}},
	}, false)

	form := url.Values{
		"id": {saved.ID}, "dbType": {"postgres"}, "host": {"h"}, "port": {"5432"},
		"dbName": {"app2"}, "user": {"u"}, "ssl": {"disable"},
		"label": {"after"}, "save": {"1"}, "savePassword": {"1"}, "password": {"pw"},
		"paramName": {"application_name"}, "paramValue": {"new"},
	}
	if res := postForm(t, srv, "/connect", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", res.StatusCode, bodyOf(t, res))
	}

	conns, err := s.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("editing created a second entry: %+v", conns)
	}
	if conns[0].ID != saved.ID {
		t.Fatalf("id changed on edit: %q -> %q", saved.ID, conns[0].ID)
	}
	if conns[0].Label != "after" || conns[0].DBName != "app2" {
		t.Fatalf("edit did not apply: %+v", conns[0])
	}
	if len(conns[0].Params) != 1 || conns[0].Params[0].Value != "new" {
		t.Fatalf("params not updated: %+v", conns[0].Params)
	}
}

func TestConnect_savingWithoutAnIDAddsASecondConnection(t *testing.T) {
	s, srv := newConnectTestServer(t)
	stubSQL(t, nil)
	if _, err := s.store.Save(SavedConnection{
		Label: "original", DBType: "postgres", Host: "h", Port: 5432, DBName: "app", User: "u",
	}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// This is what the duplicate flow posts: same settings, no id, new label.
	form := url.Values{
		"dbType": {"postgres"}, "host": {"h"}, "port": {"5432"}, "dbName": {"app_copy"},
		"user": {"u"}, "ssl": {"disable"}, "label": {"original copy"}, "save": {"1"},
	}
	if res := postForm(t, srv, "/connect", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", res.StatusCode)
	}
	conns, _ := s.store.List()
	if len(conns) != 2 {
		t.Fatalf("duplicate did not add a second connection: %+v", conns)
	}
}

// Editing a stored connection must not force a session against it: "Save" is a
// separate action from "Connect".
func TestConnect_saveActionPersistsWithoutConnecting(t *testing.T) {
	s, srv := newConnectTestServer(t)
	calls, _ := stubSQL(t, nil)
	saved, _ := s.store.Save(SavedConnection{
		Label: "before", DBType: "postgres", Host: "h", Port: 5432,
		DBName: "app", User: "u", Password: "pw",
	}, false)

	form := url.Values{
		"action": {"save"}, "id": {saved.ID},
		"dbType": {"postgres"}, "host": {"h2"}, "port": {"5433"},
		"dbName": {"app"}, "user": {"u"}, "ssl": {"disable"},
		"label": {"after"}, "savePassword": {"1"},
	}
	res := postForm(t, srv, "/connect", form)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", res.StatusCode, bodyOf(t, res))
	}
	if got := res.Header.Get("Location"); got != "/connect?mode=chooser" {
		t.Fatalf("saving should return to the chooser, got %q", got)
	}
	if len(*calls) != 0 {
		t.Fatalf("saving must not dial the database: %+v", *calls)
	}
	if len(s.sessions.All()) != 0 {
		t.Fatal("saving must not open a session")
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			t.Fatal("saving must not set the session cookie")
		}
	}

	stored, _, _ := s.store.Get(saved.ID)
	if stored.Label != "after" || stored.Host != "h2" || stored.Port != 5433 {
		t.Fatalf("save did not apply: %+v", stored)
	}
	if stored.Password != "pw" {
		t.Fatal("a save that omits the password should keep the stored one")
	}
}

// A connection can be edited even while it is unreachable — the whole point of
// separating save from connect.
func TestConnect_saveActionWorksWhenTheDatabaseIsDown(t *testing.T) {
	s, srv := newConnectTestServer(t)
	stubSQL(t, errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"))

	form := url.Values{
		"action": {"save"}, "dbType": {"postgres"}, "host": {"unreachable"},
		"port": {"5432"}, "dbName": {"app"}, "user": {"u"}, "label": {"offline box"},
	}
	if res := postForm(t, srv, "/connect", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", res.StatusCode, bodyOf(t, res))
	}
	conns, _ := s.store.List()
	if len(conns) != 1 || conns[0].Label != "offline box" {
		t.Fatalf("connections = %+v", conns)
	}
}

func TestConnect_saveActionRejectsBadParamNames(t *testing.T) {
	s, srv := newConnectTestServer(t)
	form := url.Values{
		"action": {"save"}, "dbType": {"postgres"}, "host": {"h"}, "port": {"5432"},
		"dbName": {"app"}, "user": {"u"}, "label": {"bad"},
		"paramName": {"tls=1&x"}, "paramValue": {"y"},
	}
	res := postForm(t, srv, "/connect", form)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the form re-rendered with the error", res.StatusCode)
	}
	if !strings.Contains(bodyOf(t, res), "invalid parameter name") {
		t.Fatal("the error should name the problem")
	}
	if conns, _ := s.store.List(); len(conns) != 0 {
		t.Fatalf("nothing should have been saved: %+v", conns)
	}
}

func TestConnectPage_chooserCarriesTheDialogForm(t *testing.T) {
	s, srv := newConnectTestServer(t)
	saved, _ := s.store.Save(SavedConnection{
		Label: "orders", DBType: "postgres", Host: "h", Port: 5432, DBName: "orders", User: "u",
	}, false)

	res, err := http.Get(srv.URL + "/connect")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	body := bodyOf(t, res)
	for _, want := range []string{
		`<dialog id="connection-dialog"`,
		`id="connect-form"`,
		`id="submit-save"`,
		`data-edit-connection="` + saved.ID + `"`,
		`data-duplicate-connection="` + saved.ID + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("chooser missing %q", want)
		}
	}
	// Without JavaScript the same actions still work as plain links.
	if !strings.Contains(body, `href="/connect?edit=`+saved.ID+`"`) {
		t.Fatal("edit must degrade to a plain link when the dialog is unavailable")
	}
}

// Saved connections are usable as a clone-schema target without being live
// first: the store supplies the password and parameters.
func TestResolveCloneTarget_fromSavedConnection(t *testing.T) {
	s, _ := newConnectTestServer(t)
	calls, _ := stubSQL(t, nil)
	saved, err := s.store.Save(SavedConnection{
		Label: "clone target", DBType: "postgres", Host: "h", Port: 5432,
		DBName: "target", User: "u", SSL: "disable", Password: "stored-pw",
		Params: []Param{{Name: "application_name", Value: "seedstorm-clone"}},
	}, false)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	source, err := s.sessions.OpenDSN("pgx", "postgres://u:p@h:5432/source", ConnectionInfo{DBType: "postgres", DBName: "source"})
	if err != nil {
		t.Fatalf("source session: %v", err)
	}

	target, err := s.resolveCloneTarget(CloneSchemaRequest{TargetSavedID: saved.ID}, source)
	if err != nil {
		t.Fatalf("resolveCloneTarget: %v", err)
	}
	if target.ID == source.ID {
		t.Fatal("target must be a separate session")
	}
	if target.Info.Label != "clone target" || target.Info.DBName != "target" {
		t.Fatalf("target info = %+v", target.Info)
	}
	if !strings.Contains(target.DSN, "stored-pw") {
		t.Fatalf("stored password was not used: %q", target.DSN)
	}
	if !strings.Contains(target.DSN, "application_name=seedstorm-clone") {
		t.Fatalf("saved parameters were not applied: %q", target.DSN)
	}
	if len(*calls) == 0 {
		t.Fatal("expected the target connection to be opened")
	}

	if _, err := s.resolveCloneTarget(CloneSchemaRequest{TargetSavedID: "c_missing"}, source); err == nil {
		t.Fatal("an unknown saved id should be an error")
	}
}
