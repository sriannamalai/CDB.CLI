# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- The Homebrew tap (`sriannamalai/homebrew-tap`) and Scoop bucket
  (`sriannamalai/scoop-bucket`) are not yet published; install a release
  binary from the GitHub Releases page instead.

[Unreleased]: https://github.com/sriannamalai/CDB.CLI/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/sriannamalai/CDB.CLI/compare/dc010ee...v1.0.0
