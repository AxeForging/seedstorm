# 001 — Connect flow overhaul: extra params, test connection, saved connections

> Working document. **Not committed** — this file stays untracked and out of the PR.

## Context

`seedstorm serve` opens on `/connect`, a single form that is the only door into the
whole web UI. Three problems make that door harder to walk through than it should be.

**1. No way to pass driver params from the structured form.** `buildDSN`
(`internal/web/dsn.go:15`) hardcodes the query string per driver — `sslmode` for
Postgres, `parseTime=true&multiStatements=true` for MySQL — with no seam for anything
else. Any connection that needs an extra driver parameter is unreachable through the
structured fields. Real drivers demand these routinely: `go-sql-driver/mysql` refuses
to authenticate against a `caching_sha2_password` account over a non-TLS socket unless
`allowPublicKeyRetrieval=1` is set, and against a cleartext-plugin account unless
`allowCleartextPasswords=1`. The failure mode today is a red banner quoting the driver
("please add 'allowCleartextPasswords=1' to your DSN") next to a form that has no field
to add it to. The only workaround is abandoning the structured fields entirely and
hand-assembling a raw DSN in the **Connection string** box — which also means
hand-escaping the password.

**2. Validating a connection costs a full page navigation.** The only way to find out
whether a connection works is to submit the form. `handleConnect`
(`internal/web/handlers_pages.go:22`) either opens a session and 303s to the workspace,
or re-renders the whole connect page with `Error:` set. Iterating on one wrong parameter
means a full round trip through a re-rendered page each time, and every re-render drops
the password field.

**3. Saved connections are browser-local, half-wired, and never the starting point.**
The server already supports several live connections at once — `SessionRegistry`
(`internal/web/session.go:52`) holds many, `/api/connections`, `/switch` and
`/disconnect`+`Pick` all exist, and the header menu lists them. But *saved* connections
are `localStorage` only (`PRESET_KEY = "seedstorm.connections.v1"`,
`internal/web/static/app.js:6`): they are invisible to a second browser, lost on a cache
clear, and — because sessions live only in server memory — every `serve` restart drops
all live connections while the saved list is stranded client-side. Worse, the header
menu disables any preset saved without a password (`btn.disabled = !p.password && !p.dsn`,
`app.js:202`), so a security-conscious save produces an entry that cannot be opened.
And on `/connect` itself, saved connections appear only as a bare `<select>` above the
form — the reconnect path, which is the common case after the first run, is the least
prominent thing on the page.

Affected: anyone running `seedstorm serve` against more than one database, or against
any database whose driver needs a parameter the form does not model.

## Requirements

Numbered, independently verifiable.

### Extra connection parameters

- **R1** — The connect form MUST let a user add an arbitrary number of driver
  parameters as name/value pairs, and MUST apply them for both `postgres` and `mysql`.
- **R2** — Extra params MUST be merged when connecting via the **structured fields**
  (`buildDSN`), producing a DSN whose query string contains every user-supplied pair.
- **R3** — Extra params MUST be merged when connecting via the **raw connection
  string** (`buildRawDSN`), preserving params already present in the raw string.
- **R4** — A user-supplied param MUST override the tool's default for the same key
  (e.g. `sslmode`, `parseTime`), rather than producing a duplicate key. Defaults not
  overridden MUST still be applied (`parseTime`/`multiStatements` for MySQL,
  `sslmode` for Postgres).
- **R5** — Param names and values MUST be URL-encoded per driver dialect so that values
  containing `&`, `=`, or spaces survive the round trip.
- **R6** — Blank rows (empty name) MUST be ignored, not emitted as `=value` or `&`.
- **R7** — The UI SHOULD offer driver-aware suggestions for common params
  (MySQL: `allowPublicKeyRetrieval`, `allowCleartextPasswords`, `tls`, `charset`,
  `timeout`; Postgres: `connect_timeout`, `application_name`, `search_path`), via a
  datalist — suggestions MUST NOT restrict what can be typed.
- **R8** — When a driver error names a missing parameter (the driver's message contains
  a recognisable `key=value` hint), the error surface SHOULD offer a one-click action
  that adds that param row pre-filled.

### Test connection

- **R9** — The connect page MUST provide a **Test connection** action that reports
  reachability without creating a session, without navigating, and without clearing the
  form (password included).
- **R10** — Test MUST use exactly the same DSN construction as a real connect, so a
  passing test implies a working connect for the same inputs.
- **R11** — Test MUST report success with the server-visible connection identity
  (driver, host:port, database, user) and the round-trip duration.
- **R12** — Test MUST report failure with the driver's verbatim error text, inline,
  leaving all entered values intact for editing.
- **R13** — Test MUST time out on its own (5s, matching `SessionRegistry.open`) and MUST
  NOT leave a pooled connection open afterwards.
- **R14** — Submitting the form after a failed test MUST still be possible — the test is
  advisory, never a gate.

### Saved connections

- **R15** — Connections MUST persist server-side in a file under the user's config dir
  (`$XDG_CONFIG_HOME/seedstorm/connections.yaml`, falling back to
  `~/.config/seedstorm/connections.yaml`), surviving both a browser change and a
  `serve` restart.
- **R16** — The store MUST support create, update (by id), delete, and list, exposed as
  JSON endpoints and driven from the UI.
- **R17** — A saved connection MUST record: id, label, driver, host, port, database,
  user, ssl mode, raw DSN (if that is how it was entered), and its extra params.
- **R18** — Storing the password MUST be opt-in per connection via an explicit
  checkbox. When not opted in, no password material may be written to disk.
- **R19** — The connections file MUST be created with mode `0600` and its parent
  directory with `0700`. If an existing file has broader permissions, the server MUST
  tighten them on write.
- **R20** — The UI MUST state plainly, at the point of opt-in, that the password is
  stored unencrypted on the local machine.
- **R21** — Password material MUST NOT appear in any list/read API response; the API
  MUST expose only a boolean `hasPassword`. The password leaves the store only into a
  DSN at connect time.
- **R22** — When at least one connection is saved, `/connect` MUST open on a chooser
  listing them, with the add-connection form behind an explicit "Add connection"
  action. With none saved, `/connect` MUST show the form directly.
- **R23** — Selecting a saved connection with a stored password MUST connect in one
  click. Selecting one without MUST prompt for just the password, then connect —
  it MUST NOT be rendered as disabled/unusable (fixing today's `btn.disabled` dead end).
- **R24** — Each saved connection MUST offer edit and delete from the chooser, and edit
  MUST round-trip every field including extra params.
- **R25** — Existing `localStorage` presets MUST be migrated into the server store once,
  on first load, without duplicating entries on subsequent loads.
- **R26** — Saved connections (durable, on disk) MUST be visually distinct from live
  sessions (in-memory, connected now) wherever both appear, and a saved connection that
  is currently live MUST offer "switch to" rather than a second connect.

## Non-goals

- Encrypting stored passwords, OS keyring integration, or a master password. R18/R20
  accept plaintext-on-disk behind an explicit opt-in, matching what the existing
  `localStorage` "include password" checkbox already does.
- Any authentication on the `serve` UI itself. It remains a localhost-bound,
  single-user tool (`--addr` default `127.0.0.1:8080`).
- New CLI flags on `seedstorm serve`. Connection details stay a web-UI concern; `--addr`
  remains the only flag.
- Drivers beyond `postgres` and `mysql`.
- Connection pooling/config knobs (max open conns, lifetimes).
- Sharing or syncing connections between machines.
- Redesigning the workspace, graph, or job surfaces — this spec stops at the door.

## Design

### DSN construction: one merge point

Today two functions build DSNs independently and diverge in behaviour: `buildDSN`
(structured, hardcoded params) and `buildRawDSN` → `ensureMySQLParams` (raw, substring
checks like `strings.Contains(raw, "multiStatements=")`). Both grow an `extras` input,
and the MySQL merge moves from substring inspection to real parsing.

```
buildDSN(info ConnectionInfo, password string, extras []Param) (driver, dsn string, err error)
buildRawDSN(dbType, raw string, extras []Param) (driver, dsn string, info ConnectionInfo, err error)
```

Per driver:

- **Postgres** — already assembles a `url.URL` and uses `url.Values`. Defaults go in
  first (`sslmode`), then each extra `q.Set(k, v)`, so a user-supplied `sslmode` wins
  (R4) and encoding is free (R5).
- **MySQL** — the driver DSN is `user:pass@tcp(host:port)/db?params`. Rather than
  string-appending, split on the last `?`, parse the query with `url.ParseQuery`, apply
  defaults only for keys not already present, then apply extras with `Set`, and
  re-encode. This also fixes a latent bug in `ensureMySQLParams`: a raw DSN containing
  `interpolateParams=true` currently satisfies neither `Contains("parseTime=")` nor
  `Contains("multiStatements=")` correctly only by luck of substring choice, and a DSN
  whose *database name* contains `parseTime=` would defeat the check entirely.

For the raw path, extras merge on top of what the user typed, so a raw DSN that already
carries `allowPublicKeyRetrieval=true` plus an extras row for the same key resolves to
the row's value (single key, R4).

`ConnectionInfo` gains `Params []Param` so the merged set is visible to templates, logs,
and the saved-connection store. `connectionLabelForLog` and the log path must keep
printing only non-secret fields.

**Alternative rejected — free-text query string box.** One input, `foo=1&bar=2`, is a
much smaller change, but it offers no discoverability for the exact case that motivated
this (a user staring at a driver error does not know the parameter's spelling), and it
pushes URL-encoding onto the user. Rejected in favour of key/value rows with a datalist.

**Alternative rejected — hardcoding the handful of known params as checkboxes.** Models
today's known drivers only; any new driver parameter reopens the same dead end.

### Test connection: a ping-only path

New `POST /connect/test`, accepting the same form fields as `POST /connect` and
returning JSON, never a redirect:

```json
{ "ok": true,  "driver": "mysql", "target": "user@127.0.0.1:3306/appdb", "elapsedMs": 24 }
{ "ok": false, "error": "this user requires clear text authentication...",
  "hint": { "param": "allowCleartextPasswords", "value": "1" } }
```

Implementation reuses `buildDSN`/`buildRawDSN` and then does `sql.Open` → `PingContext`
(5s, matching `SessionRegistry.open`, R13) → `Close` in a deferred call. It deliberately
does **not** go through `SessionRegistry`, so a test never registers a session, never
sets a cookie, and never affects the header connection menu (R9).

The `hint` field carries R8: a small matcher over the driver's error text extracts a
`key=value` suggestion (the MySQL driver's messages state the parameter literally), and
the UI turns it into an "add this param" button. The matcher returns nothing when it
does not recognise the text — no guessing.

Client side, the test button posts `new FormData(form)` via `fetch` and renders the
result into a status region next to the button (`aria-live="polite"`). Nothing about the
form is reset, so the password survives (R9/R12) — the concrete regression the current
full-page re-render causes.

**Alternative rejected — reusing `POST /connect` with a `test=1` field.** Keeps the route
count down, but tangles two response contracts (HTML redirect vs JSON) into one handler
and makes the "never create a session" guarantee a runtime branch rather than a separate
code path.

### Saved connections: server-side store

New `internal/web/store.go` with a `ConnectionStore` interface and a YAML file
implementation, wired through `Options` in `web.New` so tests inject a `t.TempDir()`
path and never touch the real config dir.

```yaml
version: 1
connections:
  - id: c_7f3a91
    label: local postgres
    dbType: postgres
    host: 127.0.0.1
    port: 5432
    dbName: appdb
    user: seedstorm
    ssl: disable
    params: [{name: connect_timeout, value: "5"}]
    password: ""        # present only when the user opted in
```

Writes are atomic (temp file in the same dir + `os.Rename`), the file is `0600` and the
directory `0700` (R19); on write, existing broader modes are tightened. The store is
mutex-guarded — several browser tabs can hit it concurrently.

The JSON surface (R16, R21) keeps secrets server-side:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/saved-connections` | list; each entry has `hasPassword`, never `password` |
| `POST` | `/api/saved-connections` | create (optionally with password) |
| `PUT` | `/api/saved-connections?id=` | update; omitted password keeps the stored one |
| `DELETE` | `/api/saved-connections?id=` | delete |
| `POST` | `/connect/saved` | connect using a saved id (+ password if not stored) |

Because these mutate local state from a browser, each mutating call requires an
`X-Seedstorm-Request: 1` header, which the fetch wrapper sets and a cross-site form post
cannot. Combined with the localhost bind this is proportionate for a single-user local
tool; full CSRF tokens are noted as a follow-up if the UI ever binds beyond loopback.

**Alternative rejected — keep `localStorage` as the sole store.** Smallest diff, but it
cannot satisfy R15 (survive a browser change) and leaves the awkward split where the
server owns live sessions and the browser owns saved ones — the split that produces
today's "needs secret" disabled entries.

**Alternative rejected — server file plus `localStorage` cache.** A sync story with no
payoff for a localhost tool whose server is the same process as the UI.

### Landing flow

`handleIndex` and `handleConnect` gain one branch: with a live session, the workspace as
today; with no live session but ≥1 saved connection, the **chooser**; with neither, the
form. The chooser and the form live in the same `connect` template, toggled by a
`Mode` field on `pageData` and an "Add connection" / "Back to saved" control, so no new
route or redirect rule appears (R22).

Chooser rows show label, a driver dot, `user@host:port/db`, a lock glyph when a password
is stored, and a "live" pill when a session for the same connection key already exists
(reusing `sessionConnectionKey`, R26) — in which case the primary action becomes
"Switch" (`POST /switch`) rather than a second connect. A row without a stored password
expands inline to a single password field rather than being disabled (R23), which is the
direct fix for `app.js:202`.

Migration (R25): on first chooser load, `app.js` posts any `localStorage` presets to
`/api/saved-connections`, then marks a `seedstorm.presetsMigrated.v1` flag. The server
deduplicates by connection key, so a second run cannot double-insert even if the flag is
lost.

### Intuitiveness — what "more intuitive" means concretely

Each of these is a specific defect in the current page, and each has a test in the plan:

1. Reconnecting is one click from the landing surface, not a dropdown inside a form.
2. Errors appear next to the control that caused them and never destroy typed input.
3. A driver error that names a parameter offers a button that adds that parameter.
4. Structured fields and raw DSN no longer differ in capability, so "which box do I use"
   stops mattering — a hint under the raw field states it overrides the fields above.
5. Saving a connection without its password produces a usable entry, not a dead one.
6. Default port follows the driver select (existing behaviour, kept), and driver choice
   also swaps the param suggestions and the SSL control's relevance.

## Data & API changes

**New file on disk** — `$XDG_CONFIG_HOME/seedstorm/connections.yaml` (`0600`), schema
above, `version: 1`. Absent file = empty list; unreadable/corrupt file surfaces a
non-fatal UI error and leaves the file untouched rather than overwriting it.

**New Go types** (`internal/web`):

- `Param{Name, Value string}`
- `SavedConnection{ID, Label, DBType, Host, Port, DBName, User, SSL, DSN string; Params []Param; Password string; HasPassword bool}` — `Password` is `json:"-"`.
- `ConnectionStore` interface + `fileStore` implementation.

**Changed signatures** — `buildDSN` and `buildRawDSN` take `extras []Param`;
`ConnectionInfo` gains `Params []Param`; `web.Options` gains `ConnectionsPath string`.
All are internal to `internal/web`; no exported CLI/API contract changes.

**New routes** — `POST /connect/test`, `POST /connect/saved`,
`GET|POST|PUT|DELETE /api/saved-connections`.

**No database migrations.** seedstorm never owns a schema of its own.

## Test plan

Unit tests colocate in `internal/web` (Go stdlib `testing`, no assertion framework, per
repo convention). Handler tests drive `Server.Handler()` via `httptest`. `sqlOpen`
(`session.go:23`) is already a package var, so the DB boundary can be stubbed for the
handler-level tests; DSN correctness is verified by string assertions, and the real
driver path is covered by the integration suite.

| Req | Verification |
|---|---|
| R1, R7 | `handlers_pages_test.go`: rendered connect page contains param rows + a driver-specific datalist for each driver |
| R2 | `dsn_test.go`: table test — structured info + extras → expected DSN, both drivers |
| R3 | `dsn_test.go`: raw DSN + extras → merged query, params already in the raw string preserved |
| R4 | `dsn_test.go`: extras `sslmode=require` / `parseTime=false` override defaults; key appears exactly once; untouched defaults still present |
| R5 | `dsn_test.go`: values containing `&`, `=`, space round-trip through `url.ParseQuery` |
| R6 | `dsn_test.go`: extras with empty names produce no trailing `&` / `=value` |
| R8 | `dsn_test.go`: `paramHintFromError` returns `allowCleartextPasswords=1` and `allowPublicKeyRetrieval=true` for the real driver strings, and nothing for unrelated errors |
| R9, R14 | handler test: `POST /connect/test` returns 200 JSON, sets no `seedstorm_session` cookie, leaves `sessions.All()` empty; a subsequent `POST /connect` still succeeds |
| R10 | test asserts the DSN passed to the stubbed `sqlOpen` from `/connect/test` byte-equals the one from `/connect` for identical form input |
| R11 | success payload carries `driver`, `target`, `elapsedMs` |
| R12 | stubbed open returns an error → `ok:false` with verbatim text, HTTP 200 (transport succeeded) |
| R13 | stub blocks past the deadline → request returns `ok:false` with a timeout error; stub records that `Close` was called |
| R15, R17 | `store_test.go`: write → new store instance over the same `t.TempDir()` path → all fields survive, including params |
| R16 | handler tests for create/list/update/delete round trip; delete of an unknown id is 404 |
| R18, R21 | store test: opt-out write leaves no password bytes in the file (read raw and assert); `GET` payload has `hasPassword` and no `password` key |
| R19 | store test: `os.Stat` → file `0600`, dir `0700`; pre-create the file `0644` and assert it is tightened after a write |
| R20 | template test: opt-in checkbox label contains the plaintext warning |
| R22 | handler test: empty store → form markup; one saved connection → chooser markup; live session → workspace |
| R23 | handler test: `POST /connect/saved` with a stored password opens a session; without one and with no password supplied returns a prompt state, not an error page; with the password supplied it connects |
| R24 | handler test: `PUT` round-trips every field including params |
| R25 | store test: importing the same preset twice yields one entry (dedupe by connection key) |
| R26 | handler test: with a live session matching a saved connection, the chooser row renders the switch action |

**Integration** (`integration/`, build tag `integration`, real Postgres + MySQL from
`compose.yaml`): drive the real server against the real containers — `/connect/test`
returns `ok:true` for good credentials and `ok:false` with the driver's message for a
wrong password; a structured-form connect carrying an extra param reaches the database;
a saved connection persists across a rebuilt `Server` over the same store path.

**Manual pass before the PR** (the "really intuitive" check, done in a browser against
`make dev-up`): first-run empty state → add → test → connect; second visit lands on the
chooser; add a second connection and switch between them; save without a password and
confirm the inline prompt connects; restart `serve` and confirm the chooser still lists
both; trigger a driver param error and confirm the hint button fixes it.

## Rollout

No feature flags — `serve` is a local dev tool and the UI ships whole. Order:

1. `Param` type + DSN merge (`dsn.go`, `dsn_test.go`) — self-contained, no UI.
2. `/connect/test` + param rows in the template and `app.js`. Shippable on its own;
   solves the original dead end.
3. `ConnectionStore` + JSON API + chooser landing + `localStorage` migration.

Rollback is `git revert` of the PR; the only durable artefact is
`~/.config/seedstorm/connections.yaml`, which a reverted binary simply ignores (it is
additive, never read by older code). No data loss — `localStorage` presets are copied,
not moved, so a revert leaves the browser-side list intact.

Docs: `README.md` Web UI section and `docs/commands.md` need the new connect flow
(chooser, test button, extra params) per the repo's README-in-sync rule. `serve`'s flag
table is unchanged — `--addr` remains the only flag.
