# CouchDB CLI Landscape and Stack Options

Research snapshot for CDB.CLI, taken 2026-09-08. Goal: an interactive
CLI client for Apache CouchDB that any end user can install and use,
in the spirit of `psql`, `mongosh`, `redis-cli`, and the dbcli family.

## 1. Prior art

Nothing maintained exists. Every interactive attempt died within a few
years, and the official docs still point users at `curl`.

| Tool | Language | Last activity | Type | Verdict |
|---|---|---|---|---|
| [cdbcli](https://github.com/kevinjqiu/cdbcli) | Python (prompt_toolkit) | 2021-03 | Interactive REPL, filesystem metaphor (`cd`/`ls`/`cat`), autocompletion, `$EDITOR` integration | Closest prior art. Dead 5 years. |
| [cushion-cli](https://github.com/stefanjudis/cushion-cli) | JavaScript | 2013 | Interactive prompt | Archived. |
| [couchshell](https://github.com/taktik/couchshell) | JavaScript | deprecated | Filesystem-shell REPL | Deprecated on npm. |
| [PSCouchDB](https://github.com/MatteoGuadrini/PSCouchDB) | PowerShell | 2025-02 | Cmdlet library, full API coverage | Not a REPL, Windows/PowerShell only. |
| [IBM couchbackup](https://github.com/IBM/couchbackup) | Node | 2026-09 | One-shot backup/restore | Only actively maintained tool. No attachment support. |
| [go-kivik/kouch](https://github.com/go-kivik/kouch) | Go | 2018 | curl-like one-shot | Stalled. |
| [couchdb-utils](https://github.com/awilliams/couchdb-utils) | Go | 2015 | One-shot | Dead. |
| [tuffet](https://github.com/cdaringe/tuffet) | JavaScript | 2026-02 | Full API wrapper CLI | Single-user project, 3 stars. |
| [couch-cli](https://github.com/duncanscott/couch-cli) | Groovy | 2024-12 | Replication/admin | JVM, sporadic. |
| [couchreplicate](https://github.com/glynnbird/couchreplicate) | JavaScript | 2025-09 | Replication orchestration | Single purpose. |
| usql | Go | active | Universal SQL CLI | No CouchDB driver. |
| Fauxton | Web | ships with CouchDB | Official web admin UI | The UX benchmark, not a CLI. |

### Gaps a new tool can fill

1. No maintained interactive REPL at all.
2. No Mango-aware query UX. Everyone falls back to `curl -d '{...}'`.
3. No attachment-safe backup or restore.
4. No live cluster or admin view (`_active_tasks`, scheduler, replication status).
5. No live autocompletion of database names, view names, or sampled document fields.
6. Fragmented single-purpose tools in four languages with four auth flows.
7. No full-screen, panel-based TUI browser.

### What to emulate

- **mongosh**: one binary that is a REPL, an admin console, and scriptable via pipes.
- **pgcli / mycli**: live schema autocompletion, syntax highlighting, multi-line editing.
- **redis-cli**: zero-friction static binary, `--stat`-style introspection, pipe mode.
- **usql**: driver-style separation between the shell and the backend.
- **cdbcli**: the filesystem metaphor. Three independent projects converged on it, which signals what users expect.

## 2. CouchDB API surface (as of 3.5.2, May 2026)

- Current line is **3.5.x**. 3.5 brought Nouveau on Lucene 10, HTTP/2, and QuickJS as the JS engine. 3.6 will default to QuickJS. CouchDB 4.0 is still in discussion.
- **Auth**: Basic, cookie session (`POST /_session`, default 10 min), JWT, proxy auth. Default the CLI to the session flow, store secrets in the OS keychain, never accept passwords as CLI args.
- **Server**: `/`, `_all_dbs`, `_dbs_info`, `_up`, `_active_tasks`, `_node/{n}/_stats`, `_node/{n}/_config`, `_prometheus`, `_cluster_setup`.
- **Database**: create/delete/info, `_all_docs` (plus `_partition/{p}/…`), `_bulk_docs`, `_bulk_get`, `_purge`, `_compact`, `_view_cleanup`, `_security`.
- **Documents**: CRUD with revisions, `_conflicts`, attachments (inline and streamed), ETag / `If-None-Match`.
- **Query**: Mango `_find`, `_index`, `_explain`; design docs and map/reduce views; `_search` for Nouveau/Clouseau.
- **Replication**: `_replicate`, `_replicator` db, `_scheduler/jobs`, `_scheduler/docs`.
- **Changes**: normal, longpoll, continuous, eventsource. Needs reconnect with backoff and `heartbeat` for proxies that buffer.

### Gotchas

- Paging with `skip` degrades badly and is unsafe under concurrent writes. Use `startkey`/`startkey_docid` for views and `bookmark` for Mango.
- Surface conflicts explicitly. Never silently pick a winner.
- Stream attachments both ways. Never buffer whole files.
- TLS: honour system CAs, offer an explicit insecure opt-in for local clusters, never silently disable verification.
- `_cluster_setup` is a multi-step stateful flow that suits a guided wizard.

## 3. Language and stack options

The hard constraint is "any end user can install it". That favours a
single static binary, which narrows the field to Go and Rust, with Node
via Single Executable Applications as a workable third.

### Option A: Go

| Concern | Pick | Notes |
|---|---|---|
| CouchDB client | [Kivik v4](https://github.com/go-kivik/kivik) | Mature, Apache-2.0, ctx-aware, covers Mango, changes (continuous), replication, attachments, bulk, views, security, session and basic auth. Raw `net/http` escape hatch for partitioned paths, JWT, proxy auth. Also has an in-memory driver for tests. |
| Command surface | cobra | De facto standard (gh, kubectl). Kong is a leaner runner-up. |
| REPL line editor | [reeflective/readline](https://github.com/reeflective/readline) | vi and emacs modes, completion menus, highlighting, `.inputrc`. ergochat/readline is the conservative fork of chzyer. go-prompt upstream is dead; joeycumines fork is alive but emacs only. |
| TUI | Bubble Tea v2 + Lip Gloss + Bubbles | Elm architecture, testable. tview is the faster-prototype alternative (k9s uses a fork). |
| Rendering | chroma (JSON highlighting), go-pretty/table, glamour, shell out to `$PAGER` | Same choices usql makes. |
| Config / secrets | koanf, 99designs/keyring | Lighter than viper; keyring covers macOS, Secret Service, KWallet, Windows, encrypted-file fallback. |
| Distribution | goreleaser | Static `CGO_ENABLED=0` builds, GitHub releases, Homebrew tap, Scoop in one config. |
| Exemplars | xo/usql, k9s, gh, lazygit | usql is the closest architectural precedent. |

Strengths: the most complete CouchDB client in any language, fastest
path to a working tool, largest contributor pool, binaries around
10 to 20 MB. Weakness: Go line editors are less polished than reedline.

### Option B: Rust

| Concern | Pick | Notes |
|---|---|---|
| CouchDB client | Hand-rolled on reqwest (rustls) + serde_json | [couch_rs](https://github.com/mibes/couch-rs) 0.13 (Jan 2026) is pre-1.0 with thin docs and no visible changes-feed streaming or replication API. sofa and couchdb crates are dead. The HTTP API is small enough that a typed in-repo client is the better bet. |
| Command surface | clap v4 derive + clap_complete | Standard. |
| REPL line editor | [reedline](https://github.com/nushell/reedline) 0.51 | Best-in-class: highlighting, completion menus, multi-line, history search, hints, vi/emacs. Shares crossterm with ratatui. rustyline is the classic fallback. |
| TUI | ratatui 0.30 + crossterm + tui-tree-widget + ratatui-textarea | Very active ecosystem. cursive is stalled. |
| Rendering | colored_json or syntect, comfy-table, minus (pager), indicatif, owo-colors/anstyle, jaq for jq-style filtering | jaq gives users jq muscle memory inside the shell. |
| Config / secrets | figment + figment-keyring, directories, keyring 4.x, secrecy | |
| Distribution | cargo-dist + cargo-zigbuild, cargo-binstall, Homebrew tap | |
| Exemplars | nushell, atuin, gitui, xh, `surreal sql` | atuin and gitui are the closest in shape. |

Strengths: the best REPL editor available anywhere, smallest binaries,
strongest correctness story for streaming and memory. Weakness: the
CouchDB client has to be written from scratch, and compile times and
contributor pool are worse than Go.

### Option C: Node/TypeScript with SEA

nano 11 is the official, healthy CouchDB client. Ink for TUI,
Commander for args, `@inquirer/prompts` for prompts. Node 22+ Single
Executable Applications are stable and mongosh proves the pattern.
Weaknesses: binaries of 80 to 100 MB, slower startup, `vercel/pkg` is
deprecated, and Bun compile has open standalone-binary issues.

### Option D: Python with prompt_toolkit

pgcli, mycli, and litecli are the gold standard REPL UX, and Textual
plus Rich is an excellent TUI stack. Clients: aiocouch, couchdb3, or
hand-rolled httpx (couchdb-python was archived Sep 2025). Weakness:
needs a Python runtime, so `uv tool install` or pipx is the real
install path. PyInstaller binaries are large and fragile. Fails the
"any end user" test unless you accept a runtime dependency.

## 4. Recommendation

**Go**, with Kivik v4, cobra, reeflective/readline, Bubble Tea, and
goreleaser. Reasons:

1. Kivik is the only mature, maintained CouchDB client in any compiled
   language. Rust would spend the first milestone rebuilding it.
2. The repo already carries a Go gitignore.
3. usql and k9s are direct templates for the REPL and TUI shapes.
4. goreleaser gives Homebrew, Scoop, and static binaries for free.

**Rust** is the close second and the right call if REPL polish
(reedline) and binary size matter more than time to first release, or
if the maintainer prefers Rust day to day.

## 5. Suggested product shape

Regardless of language, the research points at one binary with three
modes:

- **One-shot subcommands** for scripts and pipes: `cdb dbs`, `cdb get mydb doc1`, `cdb find mydb '{"selector":…}'`, `cdb changes mydb --follow`, `cdb backup`, `cdb restore`.
- **Interactive shell** as the default: filesystem metaphor (`cd`, `ls`, `cat`), live completion of db, view, and field names, Mango and view queries, `$EDITOR` for doc edits, jq-style filtering, history.
- **TUI browser** (`cdb ui` or a key from the shell): db list, doc viewer with JSON highlighting, results table, changes tail, active tasks and replication status.

Differentiators none of the prior art delivers: Mango-aware
completion, attachment-safe backup and restore, conflict resolution
workflow, and a live cluster and replication view.
