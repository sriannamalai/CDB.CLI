# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- The Homebrew cask installs on Linux. Its post-install step ran
  `/usr/bin/xattr` to clear the macOS quarantine attribute and failed with
  exit 127 where that file does not exist; it now runs on macOS only.
- The interactive shell no longer flashes the line and swallows typed
  characters over a terminal with any latency, such as one across an SSH link.
  It asked the terminal where the cursor was on every keystroke and waited for
  the answer in the middle of the redraw, and a character typed during that
  wait was thrown away with the answer.

## [1.1.1] - 2026-09-09

### Fixed

- `tail --include-docs` leaves the doc cell empty for a change that carried no
  document, and leaves the key out of the row JSON, instead of printing the
  literal `null` and emitting `"doc":null`.
- `resolve`'s chooser renders a field whose value is JSON `null` as `null`,
  instead of `added: <field>` with a trailing space where the value belongs.
- Ctrl-C at `resolve`'s "Keep which revision?" prompt aborts quietly with exit
  130, instead of reporting that the answer was not a number between 1 and 2.
- `replicate --filter` refuses a filter that is not `<design>/<name>` with both
  halves, the way `tail --filter` already did, rather than passing it to the
  server and reporting the resulting 400 as a command failure.
- Tab completion inside a partition completes a document id typed with its
  partition key already in front (`p1:ord<Tab>`), a form `cat` and `cd` have
  always accepted; it used to offer nothing.

### Changed

- `profiles add <name> <url>` asks for the user name and password, with echo
  off, when the URL carries none, stdin is a terminal and `--anonymous` is not
  given. The credentials are verified against the server before anything is
  written and the password goes to the OS keychain, so the saved profile is one
  that can log in; a bare URL used to be saved as a session profile with no
  secret. `--anonymous` saves a profile with auth `none`, and without a
  terminal nothing is asked and the URL is stored as typed.

## [1.1.0] - 2026-09-09

### Added

- `tail`: read a database's `_changes` feed. `--follow` opens CouchDB's
  continuous feed from `now` and keeps reading, printing each change the
  moment it arrives rather than buffering a page of them, and reconnecting
  from the last change it showed after 1s, 2s, 4s, 8s, 16s and then every
  30s — a deleted database or a rejected token ends the command instead.
  `--since`, `--limit`, `--include-docs`, `--filter ddoc/name` and
  `--heartbeat` shape the read; `--json` emits one change per line.
- `--replication-url`, the profile key `replication_url` and
  `CDB_REPLICATION_URL`: the address the server should use to reach itself
  when `replicate` or `cp` writes a replication endpoint. This is the fix for
  the same-server replication limitation 1.0 shipped with — a container
  published on `localhost:15984` that calls itself `http://couchdb:5984`, an
  SSH tunnel, a reverse proxy. The value must be a bare http(s) server
  address and may not carry a user name or password; credentials still travel
  in CouchDB's per-endpoint auth object. `info /` shows it when it differs
  from the connected URL.
- `backup --tombstones` dumps deleted documents as records carrying
  `_deleted: true` and their full revision history, so a restore reproduces
  the deletion and replicates it onward — the other limitation 1.0 shipped
  with. The dump header records whether deletions were dumped and the footer
  counts them; `--resume` refuses to continue a dump in the other mode, and
  `restore` reports how many of the documents it loaded were deletions.
- Partition-scoped paths: `/db/_partition/<key>/<id>` addresses the document
  `<key>:<id>`, `/db/_partition/<key>/<id>/<name>` its attachment, and
  `/db/_partition/<key>/_design/<ddoc>/_view/<name>` runs a view against the
  partition. `ls`, `cat`, `cd`, `rm`, `edit`, `put`, `attach`, `fetch`,
  `find`, `query` and tab completion all reach through them. An id that
  already carries the `<key>:` prefix is used as it is, so a path built by
  pasting an id out of `ls` names the same document.
- `resolve` shows how each conflicting revision differs from the current one —
  field by field, at the top level, with values summarised — instead of an
  80-character preview of each body. `--diff-full` prints each revision in
  full instead.
- Dump format 1.1: the header records the format version and whether
  deletions were dumped; the footer counts them. Dumps written by 1.0 restore
  unchanged, and a dump written by a newer cdb is refused by name rather than
  half-read.

### Fixed

- `find` on a path that is not a database or a partition is a usage error
  naming the kind, instead of silently querying the whole database the
  document lives in.
- Tab completion of an attached flag value (`--fields=na`) reaches the
  command's own completer instead of matching no flag name, and completion of
  a comma-separated `--fields`/`--sort` list keeps the fields already typed.
- `cdb` now proves JWT authentication against a live CouchDB in CI: the
  handler is enabled on the service container, a hand-minted HS256 token is
  accepted, and an expired one is reported as an authentication failure
  (exit 3) rather than a server error.

### Changed

- Nothing removed or renamed. No flag changes meaning and no default changes.
  Every 1.0 invocation produces 1.0 output, with two visible additions:
  `info /` may show a `replication url` row, and `resolve`'s chooser shows
  diffs rather than body previews.

[Unreleased]: https://github.com/sriannamalai/CDB.CLI/compare/v1.1.1...HEAD
[1.1.1]: https://github.com/sriannamalai/CDB.CLI/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/sriannamalai/CDB.CLI/compare/v1.0.1...v1.1.0

## [1.0.1] - 2026-09-08

### Fixed

- Shell history no longer rewrites an ordinary argument that happens to look
  like `user:pass@host`. `ls /db --start a:b@c` is stored exactly as typed;
  a credential without a scheme is only recognised under the commands that
  take a server address (`connect`, `profiles add`, `replicate`, `cp`), and a
  URL carrying a scheme is still redacted wherever it appears.
- A missing attachment below a design document now says which design document
  was searched — `Attachment "logo.png" was not found on design document
  "app".` — instead of naming the database, so a mistyped view path is
  recognisable for what it is.
- A `401` from a connection with no credentials (`--anonymous`, or a profile
  whose auth is `none`) now reads `The server requires credentials for … .
  Connect with a username and password, or set CDB_USER and CDB_PASSWORD.`
  rather than advising you to check a password you never supplied.
- `edit` understands a quoted `$EDITOR` or `$VISUAL`, so an editor whose path
  contains a space works: `EDITOR='"/Applications/My Editor/bin/ed" -w'`.
- `put <path>` run interactively — in the shell, or as a one-shot command at a
  terminal — now asks for a file, or an explicit `-`, rather than silently
  reading the terminal to end-of-file and leaving the prompt gone. Piped use is
  unchanged: `echo … | cdb put /db/doc` still works.
- Closing a connection releases its sockets. On an authenticated client the
  close never reached the connection pool, so idle connections were held until
  the process exited.
- `restore` compares a complete dump's footer counts with what it actually
  loaded and fails, naming both numbers, when they disagree — a dump that has
  lost records no longer reports success.
- `restore` bounds the memory one bulk write holds by the base64 size the
  request really carries. Reaching the bound closes the batch, so attachments
  small enough to inline stay inline and keep the revision the dump recorded;
  only a single document whose attachments alone exceed the bound sends the
  remainder as separate uploads.

## [1.0.0] - 2026-09-08

Initial release of `cdb`, a single-binary command line client for Apache
CouchDB 3.2 through 3.5.

### Added

- Interactive shell with a filesystem metaphor: `cd`, `pwd`, `ls` and `info`
  navigate servers, databases, documents and views the way a shell navigates
  directories.
- Tab completion for command names, flags, database names, document ids,
  view names, and document field names sampled live from the database.
- A `history` command backed by a persistent history file, with repeated and
  unparseable lines skipped and any credential in a typed URL redacted
  before the line is ever written to disk.
- `| .field` style jq filters on shell output for pulling a single field out
  of a document.
- Ctrl-C abandons the running command and returns to the prompt; Ctrl-D
  exits the shell.
- Every shell command is also a one-shot subcommand, so `cdb` works
  unmodified in scripts and pipelines; when stdout is not a terminal, output
  is compact JSON, one document per line.
- Documented exit codes: `0` success, `1` command error, `2` usage error,
  `3` connection or authentication error, `130` interrupted.
- Connection profiles stored in `config.toml`, with secrets kept out of the
  file and held in the OS keychain (macOS Keychain, Windows Credential
  Manager, Secret Service or KWallet on Linux, or an encrypted file backend
  when none of those is available) under service `cdb`, keyed by profile
  name.
- Environment variable overrides for scripts and CI: `CDB_PROFILE`,
  `CDB_URL`, `CDB_USER`, `CDB_PASSWORD`, `CDB_TOKEN`, `CDB_INSECURE_TLS`,
  `CDB_KEYRING_BACKEND` and `CDB_KEYRING_PASSPHRASE` (for the encrypted
  file keyring backend). An explicit `--profile` or `--url` on the command
  line wins over `CDB_PROFILE`/`CDB_URL`, which in turn win over the
  config file.
- Session-cookie (`session`) and JWT (`jwt`) authentication, plus a
  guided `connect` walkthrough that prompts for a URL, auth kind, user name
  and password (echo off) the first time, verifies them against the server,
  and offers to save the result as a profile.
- `--anonymous` flag to connect without credentials and skip the
  credential prompts outright, including on a bare URL that carries none.
- Database commands: `ls`, `info`, `mkdir`, `rmdir`, `cp`.
- Document commands: `cat`, `put`, `rm`, `edit`, `find` (Mango queries,
  with a guided selector builder), `query` (map/reduce views).
- Attachment commands: `attach` and `fetch`, both streamed rather than
  buffered, with fetched files created at permission `0600`.
- Conflict commands: `conflicts` to list conflicting revisions and
  `resolve` to pick a winner.
- `backup`: a resumable, gzip-compressed dump of a database's live
  documents and their attachments, restartable with `--resume`.
- `restore`: revision-preserving load from a `cdb` dump via `_bulk_docs`
  with `new_edits=false`, recreating any conflicts recorded in the dump.
- `replicate` and `replications` to start, list, watch and cancel
  replication jobs, using credentialed same-server endpoints when both
  databases live on the server `cdb` is connected to (CouchDB does not
  accept a bare database name).
- Plain-sentence, operator-facing error messages in place of raw CouchDB
  JSON errors, with `--verbose` available to append the raw status, error
  name and server reason.
- A paged table renderer that streams output in chunks (200 rows at a
  time) instead of buffering an entire result set before printing, so
  `ls --all` on a large database starts printing immediately.
- `--format table|json`, `--color auto|always|never` (honouring
  `NO_COLOR`) and `--pager <cmd>|off` for one-shot output control.
- Shell completion scripts for bash, zsh, fish and PowerShell via
  `cdb completion <shell>`.
- goreleaser-built release binaries for macOS (arm64, amd64), Linux
  (amd64, arm64) and Windows (amd64, arm64), published with checksums to
  GitHub Releases.

### Security

- Passwords are never written to `config.toml`; only a URL and username
  are stored there; the password itself goes to the OS keychain (or the
  encrypted file backend) keyed by profile name.
- A credential typed as part of a URL — on the command line, at a prompt,
  or in `profiles add` — is split out before anything is persisted, and
  is never echoed back to stdout.
- Shell history redacts the userinfo of any URL or schemeless
  `user:pass@host` token before writing a line to the history file, while
  leaving JSON arguments (including ones containing `@`) untouched.
- Error messages, including `--verbose` output, never include a password
  or token; URLs are redacted before they reach stderr, including on
  malformed-URL and login-failure paths.
- `config.toml` is written atomically (temp file + rename) and enforced
  at mode `0600`, even if the operator's existing file was more
  permissive.

### Known limitations

- Attachments over 4 MiB are restored in a separate step after the
  document, which bumps the document's revision from the one recorded in
  the dump.

  The first two limitations recorded here were addressed in 1.1.0 by
  `--replication-url` and `backup --tombstones`.

[1.0.1]: https://github.com/sriannamalai/CDB.CLI/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/sriannamalai/CDB.CLI/compare/dc010ee...v1.0.0
