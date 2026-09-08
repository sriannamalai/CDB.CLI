# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- `put <path>` inside the shell now asks for a file, or an explicit `-`, rather
  than silently reading the terminal to end-of-file and leaving the prompt
  gone. One-shot use is unchanged: `echo … | cdb put /db/doc` still works.
- Closing a connection releases its sockets. On an authenticated client the
  close never reached the connection pool, so idle connections were held until
  the process exited.
- `restore` compares a complete dump's footer counts with what it actually
  loaded and fails, naming both numbers, when they disagree — a dump that has
  lost records no longer reports success.
- `restore` bounds the memory one bulk write holds by the base64 size the
  request really carries, and a single document whose attachments exceed that
  bound now sends the remainder as separate uploads instead of building one
  enormous request.

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

- Same-server replication or copy jobs hand CouchDB the URL `cdb` itself
  connected with; if the server cannot reach that address from where it
  runs (a remapped Docker port, an SSH tunnel), the job is accepted and
  then fails with `econnrefused` — check it with `replications show <id>`.
- `backup` dumps carry no deletion tombstones, and a conflicted document
  is counted once per leaf revision rather than once per document.
- Attachments over 4 MiB are restored in a separate step after the
  document, which bumps the document's revision from the one recorded in
  the dump.

[Unreleased]: https://github.com/sriannamalai/CDB.CLI/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/sriannamalai/CDB.CLI/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/sriannamalai/CDB.CLI/compare/dc010ee...v1.0.0
