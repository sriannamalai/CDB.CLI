# cdb: Interactive CouchDB CLI — Design

Status: approved design, 2026-09-08. Companion research:
`docs/research/CouchDbCliLandscape.md`.

## 1. Goal

`cdb` is a single-binary command-line client for Apache CouchDB 3.x
that any end user can install and use. It is at once an interactive
shell with a filesystem metaphor and a set of one-shot subcommands for
scripts. A full-screen TUI is a later milestone and is out of scope
here.

Primary users are developers, database administrators, and
non-technical operators. The last group drives the defaults: tables
over raw JSON, confirmations on destructive actions, guided prompts
when arguments are missing, and error messages in plain sentences.

## 2. Decisions already made

| Decision | Choice |
|---|---|
| Language | Go 1.27, module `github.com/sriannamalai/CDB.CLI` |
| Binary name | `cdb` |
| v1 modes | Interactive shell (default) plus one-shot subcommands |
| Deferred | Bubble Tea TUI, changes-feed `tail`, cluster and server admin, proxy auth, Cloudant IAM |
| Audience | Developers, DBAs, and non-technical operators |
| Auth | Username and password via `_session` cookie; JWT bearer token |
| Server versions | CouchDB 3.2 through 3.5. No 2.x support. |
| v1 admin features | Backup and restore with attachments; replication create and status |
| Architecture | One command registry with two front-ends (cobra and shell) |

## 3. Stack

| Concern | Library |
|---|---|
| CouchDB client | `net/http` hand-rolled client in `internal/couch` |
| Commands | `github.com/spf13/cobra` |
| Line editor | `github.com/reeflective/readline` |
| JSON highlighting | `github.com/alecthomas/chroma/v2` |
| Tables | `github.com/jedib0t/go-pretty/v6/table` |
| jq-style filters | `github.com/itchyny/gojq` |
| Config | `github.com/knadh/koanf/v2` with the TOML parser |
| Secrets | `github.com/99designs/keyring` |
| Release | goreleaser |

Library choices are defaults, not contracts. A task may swap one if the
replacement is justified in the plan.

The CouchDB client started as `github.com/go-kivik/kivik/v4` with a raw
`net/http` escape hatch. In practice every request cdb makes needs the
escape hatch — `_dbs_info`, `_scheduler`, partitioned paths, streamed
attachments, `_bulk_docs` with `new_edits=false`, `open_revs` — so the
Kivik client ended up making no requests at all and was dropped. The
one thing it was still used for, `kivik.HTTPStatus` error
classification, reported 500 for anything it did not recognise, which
mislabelled connection failures as server errors; `internal/couch`
classifies them itself.

## 4. Architecture

### 4.1 Package layout

```
cmd/cdb/main.go            entry point; builds the cobra root and runs it
internal/couch/            CouchDB client over net/http
internal/path/             virtual filesystem paths and resolution
internal/command/          the command registry and every command
internal/session/          connection, current path, preferences
internal/cli/              cobra tree generated from the registry
internal/shell/            readline loop, parsing, completion, filters
internal/render/           table, JSON, and raw renderers; pager; colour
internal/config/           profiles file, env overrides, keyring
internal/backup/           dump and restore format and streaming
internal/replicate/        _replicator and _scheduler helpers
```

Dependencies flow downward: `cli` and `shell` depend on `command`,
which depends on `session`, `couch`, `path`, `render`, `backup`, and
`replicate`. Nothing above `couch` speaks HTTP.

### 4.2 The command registry

Each command is a value of one type:

```go
type Command struct {
    Name        string
    Aliases     []string
    Summary     string
    Usage       string          // argument synopsis, e.g. "<path> [file]"
    Flags       func(*pflag.FlagSet)
    MinArgs     int
    MaxArgs     int             // -1 for unbounded
    Complete    func(ctx, *session.Session, args []string, cur string) []Candidate
    Run         func(ctx, *session.Session, Invocation) (Result, error)
    ShellOnly   bool            // help, history, exit
    Destructive bool            // prompts for confirmation unless --yes
}
```

`Invocation` carries parsed args, parsed flags, stdin, and whether the
call came from the shell or cobra. `Result` is a renderable value: a
document, a list of rows with column metadata, a stream of rows, or a
plain message. Commands never print. They return a `Result` and the
front-end renders it.

The registry is a map from name and alias to `Command`. `cli` walks
it to build one cobra subcommand per entry, wiring flags through the
same `Flags` func. `shell` splits each input line into argv, looks up
the first token, parses flags with the same func, and calls `Run`.
Both front-ends therefore run identical code.

### 4.3 Session

`session.Session` holds the active `couch.Client`, the active profile
name, the current virtual path, output preferences (format, colour,
pager, `--yes`), and an `io.Writer` pair for stdout and stderr. The
shell creates one session for its lifetime. Cobra creates one per
process and sets the current path from `--path` or the first argument.

## 5. Virtual filesystem

### 5.1 Paths

| Path | Target |
|---|---|
| `/` | Server |
| `/mydb` | Database |
| `/mydb/doc1` | Document |
| `/mydb/_design/app` | Design document |
| `/mydb/_design/app/_view/by_date` | View |
| `/mydb/doc1/photo.jpg` | Attachment |
| `/mydb/_partition/p1` | Partition (lists docs in that partition) |

Paths are absolute when they start with `/`, otherwise relative to the
session's current path. `.` and `..` work. Database names that contain
`/` (CouchDB permits `a/b`) must be URL-encoded in paths, matching the
HTTP API. `path.Resolve(base, input) (Target, error)` returns a typed
target and is the only place path grammar lives.

### 5.2 Prompt

The shell prompt is `user@host:/current/path> `. When no connection is
active it is `cdb> `.

## 6. Command set

Every command is available as `cdb <name> …` and inside the shell as
`<name> …`. Paths default to the current path. `--json` forces raw JSON
output. `--yes` skips confirmations.

### 6.1 Connection

- `connect [profile | url]`: opens a connection. With no argument and
  exactly one profile, uses it. With no profile at all, prompts for
  URL, auth kind, username, and secret, verifies with `GET /_session`,
  and offers to save the profile and secret.
- `profiles [list | add | remove | default]`: manages saved profiles.
- `session`: shows the current user, roles, and server version.

### 6.2 Navigation and listing

- `cd <path>`: changes the current path. Verifies the target exists.
- `pwd`: prints the current path.
- `ls [path] [--limit N] [--start KEY] [--all]`: at `/` lists databases
  with doc count and size from `_dbs_info`. In a database lists
  documents by id with rev and, if `--fields a,b` is given, those
  fields. In a design doc lists views, updates, and filters. Pages with
  `startkey_docid`; the shell prints the first page and shows a hint to
  continue with `--start`. In non-terminal mode `--all` streams
  everything.
- `info [path]`: database or server info as a key-value table.

### 6.3 Querying

- `find [path] [selector-json] [--fields] [--sort] [--limit] [--bookmark] [--explain]`:
  runs a Mango `_find`. With no selector on a terminal, walks through a
  guided builder: field, operator, value, add another. Pages with
  bookmarks.
- `query <view-path> [--key] [--startkey] [--endkey] [--reduce] [--group-level] [--include-docs] [--limit] [--descending]`:
  runs a view.

### 6.4 Documents

- `cat <path> [--rev R] [--revs] [--conflicts]`: prints a document or
  attachment. Documents render as highlighted JSON on a terminal and
  raw JSON when piped.
- `edit <path>`: fetches the document, opens `$EDITOR` on a temp file,
  validates JSON on save, PUTs with the fetched rev. On 409, reloads
  and asks to reapply or discard.
- `put <path> [file]`: creates or updates a document from a file or
  stdin. Preserves `_rev` if present, otherwise fetches the current
  rev and asks before overwriting.
- `rm <path>`: deletes a document or attachment. Destructive.
- `cp <src> <dst>`: copies a document (`COPY` request) or, when both
  are databases, starts a one-shot replication.
- `attach <doc-path> <file> [--name]`: uploads an attachment, streamed.
- `fetch <attachment-path> [out-file]`: downloads an attachment,
  streamed. Defaults to the attachment's name in the current directory.
- `conflicts [path]`: lists documents with `_conflicts` in a database,
  or the conflicting revisions of one document.
- `resolve <doc-path> [--keep REV]`: shows conflicting revisions side
  by side, asks which to keep, deletes the rest.

### 6.5 Databases

- `mkdir <path> [--partitioned] [--q N]`: creates a database.
- `rmdir <path>`: deletes a database. Destructive; requires typing the
  database name to confirm on a terminal.

### 6.6 Admin

- `backup <db-path> <file> [--resume] [--since SEQ]`: see section 9.
- `restore <file> <db-path> [--create] [--merge]`: see section 9.
- `replicate <source> <target> [--continuous] [--create-target] [--filter] [--id]`:
  writes a document to `_replicator`. Source and target accept virtual
  paths for the current server or full URLs.
- `replications [list | show ID | cancel ID] [--watch]`: reads
  `_scheduler/docs` and `_scheduler/jobs`. `--watch` redraws every two
  seconds until interrupted.

### 6.7 Shell only

- `help [command]`, `history`, `clear`, `exit` / `quit`.
- Filters: `cmd … | <gojq expr>` applies the expression to the
  command's JSON result before rendering. Only one filter stage is
  supported.

## 7. Shell behaviour

- reeflective/readline with emacs bindings by default, vi via config.
- History in `$XDG_STATE_HOME/cdb/history`, deduplicated, excluding
  lines that failed to parse.
- Completion: command names, flags, then context-sensitive paths.
  Database names come from `_all_dbs`, cached for the session and
  refreshed after `mkdir` and `rmdir`. Document ids complete from a
  prefix query on `_all_docs`. View names complete from the design
  doc. Field names for `find --fields` and `--sort` complete from a
  sample of up to 50 documents, cached per database.
- Ctrl-C cancels the running command's context and returns to the
  prompt. Ctrl-D at an empty prompt exits.
- Multi-line input: a line ending in `\` continues; an unbalanced JSON
  object also continues until balanced.

## 8. Configuration and credentials

Config file: `$XDG_CONFIG_HOME/cdb/config.toml` (macOS: same path
under `~/.config`, Windows: `%APPDATA%\cdb\config.toml`).

```toml
default = "local"

[profiles.local]
url = "http://localhost:5984"
auth = "session"          # session | jwt | none
username = "admin"
insecure_tls = false
ca_file = ""

[output]
format = "table"          # table | json
color = "auto"            # auto | always | never
pager = "auto"

[shell]
keymap = "emacs"          # emacs | vi
```

Secrets are stored in the OS keychain under service `cdb` and key
`<profile>`. Backends in order: macOS Keychain, Windows Credential
Manager, Secret Service, KWallet, then an encrypted file under the
config directory with a passphrase prompt. Secrets are never written to
the config file, never accepted as flags, and never echoed.

Environment overrides for scripts: `CDB_PROFILE`, `CDB_URL`,
`CDB_USER`, `CDB_PASSWORD`, `CDB_TOKEN`, `CDB_INSECURE_TLS`. An
environment override wins over the file and does not touch the
keychain.

## 9. Backup and restore

Format: a single file, gzip-compressed, containing a stream of
newline-delimited JSON records. Record kinds:

- `{"kind":"header","db":"mydb","server":"3.5.2","started":"…","partitioned":false}`
- `{"kind":"doc","doc":{…full document including _rev and _revisions…}}`
- `{"kind":"att","id":"doc1","rev":"2-abc","name":"photo.jpg","content_type":"image/jpeg","length":12345}`
  followed by exactly `length` raw bytes and a newline.
- `{"kind":"checkpoint","seq":"…"}` after every batch.
- `{"kind":"footer","docs":N,"attachments":M,"last_seq":"…"}`

Backup reads `_changes?feed=normal&style=all_docs` in batches for the ids
and leaf revisions, then fetches each revision's full body through
`_bulk_get?revs=true`. It deliberately does not ask `_changes` for
`include_docs=true`: CouchDB ignores `revs=true` on that endpoint
(verified on 3.5.2), so the documents it returns carry no `_revisions`
and a restore could not preserve their revision history. Attachments are
fetched per-attachment and streamed straight to the file, and a
checkpoint is written after each batch. `--resume` reads the last checkpoint in an
existing file and continues from that sequence, appending.

Restore streams the file, writes documents through `_bulk_docs` with
`new_edits=false` so revisions are preserved, then uploads attachments
for that batch with the restored rev. `--create` creates the target
database, partitioned if the header says so. Restore refuses a
non-empty target unless `--merge` is given.

Progress is shown with document and byte counts; both commands are
safe to interrupt.

## 10. Client layer

`couch.Client` is one `http.Client` with a cookie jar and an
authenticating transport; every request cdb makes goes through it.
Public surface is the small set the commands need, expressed in domain
terms (`ListDatabases`, `AllDocs` with a cursor, `Find` with a
bookmark, `Get`, `Put`, `Delete`, `Copy`, `Changes`, `Attachment`
streams, `BulkDocs`, `SchedulerDocs`, and so on). The endpoints CouchDB
clients usually leave out — JWT header injection, `_dbs_info`,
`_scheduler`, partitioned paths — are just more requests on the same
client.

Session auth: `POST /_session` on connect; the cookie lives in the jar.
On any 401 the client re-authenticates once with the stored secret and
retries. JWT auth: an `Authorization: Bearer` header via a custom
`RoundTripper`. TLS: system roots, optional `ca_file`, and an explicit
`insecure_tls` that logs a warning on every connect.

Every call takes a `context.Context`. Listing calls return iterators
so renderers can stream; nothing loads an unbounded result into
memory. Paging uses `startkey_docid` for `_all_docs` and views and
`bookmark` for Mango. `skip` is never used.

## 11. Rendering and errors

`render.Result` is rendered by one of three renderers chosen by the
session: `table` (default on a terminal), `json` (pretty, highlighted
with chroma on a terminal), `raw` (compact JSON, default when stdout is
not a terminal or `--json` is set). Colour follows `NO_COLOR`,
`--color`, and terminal detection. Output longer than the terminal
height goes to `$PAGER` (default `less -R`) on a terminal.

Errors are `couch.Error{Status, Name, Reason, Op, Target}`. The
renderer maps common cases to plain sentences and keeps the raw status
and reason behind `--verbose`:

| Case | Message |
|---|---|
| 401 after re-auth | `Login failed for admin at localhost:5984. Check the password with "profiles" or "connect".` |
| 403 | `You do not have permission to <op> <target>.` |
| 404 db | `Database "mydb" does not exist. "ls /" lists databases.` |
| 404 doc | `Document "doc1" was not found in "mydb".` |
| 409 | `Document "doc1" was changed by someone else. "cat doc1" shows the latest version.` |
| connection refused | `Could not reach localhost:5984. Is CouchDB running?` |

Exit codes for one-shot mode: 0 success, 1 command error, 2 usage
error, 3 connection or auth error, 130 interrupted.

## 12. Testing

- `internal/command`: every command runs against the `couchtest`
  route-stub server in unit tests, so the whole registry is exercised
  without a real server and each test asserts the exact request cdb
  made. Destructive commands are tested with and without `--yes`.
- `internal/couch`: integration tests against CouchDB 3.5 in Docker,
  skipped unless `CDB_TEST_URL` is set. CI runs them via a service
  container. Covers session re-auth, JWT header, paging, attachment
  streaming, and `_bulk_docs` with `new_edits=false`.
- `internal/path` and `internal/shell` parsing: table-driven unit tests.
- `internal/render`: golden-file tests for table and JSON output with
  colour on and off.
- `internal/backup`: round-trip test that writes and reads real gzip dump
  files against the `couchtest` stub, plus a resume test that truncates a
  file mid-stream.
- End-to-end: a small script exercises `cdb` subcommands against the
  Docker server in CI.

## 13. Distribution

goreleaser builds `CGO_ENABLED=0` binaries for darwin, linux, and
windows on amd64 and arm64, publishes GitHub releases with checksums,
and maintains a Homebrew tap (`sriannamalai/tap/cdb`) and a Scoop
bucket. `cdb completion <shell>` emits scripts for bash, zsh, fish,
and PowerShell. Version, commit, and date are injected with ldflags
and shown by `cdb version`.

## 14. Out of scope for v1

Bubble Tea TUI, changes-feed `tail`, cluster setup wizard, `_config`
editing, user management, `_active_tasks`, compaction, proxy auth,
Cloudant IAM, Nouveau or Clouseau search, CouchDB 2.x, multi-stage
shell pipelines, scripting language or plugins.
