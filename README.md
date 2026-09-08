# cdb

`cdb` is a single-binary command line client for Apache CouchDB 3.2 through 3.5.
It is an interactive shell with a filesystem metaphor and a set of one-shot
subcommands for scripts.

## Install

Homebrew:

```
brew tap sriannamalai/tap
brew trust sriannamalai/tap
brew install --cask cdb
```

Homebrew 6 refuses to load casks from third-party taps until the tap is
trusted, and `brew trust` records that decision in `~/.homebrew/trust.json`.

Scoop:

```
scoop bucket add sriannamalai https://github.com/sriannamalai/scoop-bucket
scoop install cdb
```

From source (Go 1.27 or newer):

```
go install github.com/sriannamalai/CDB.CLI/cmd/cdb@latest
```

Or download a binary from the [releases page](https://github.com/sriannamalai/CDB.CLI/releases)
and verify it against `checksums.txt`.

The Homebrew and Scoop lines above work now. A release binary is also
available from the GitHub Releases page.

## Documentation

- [User guide](docs/UserGuide.md) — installing, connecting, the virtual
  filesystem, documents, queries, attachments, conflicts, backup and restore,
  replication, the shell, scripting, errors and troubleshooting.
- [Command reference](docs/reference/README.md) — one page per command with
  its flags, argument rules and a worked example, generated from the registry
  the binary itself runs on.

## Quickstart

`cdb` starts the interactive shell only when both stdin and stdout are a
terminal; otherwise it runs the one-shot subcommand named on the command line.
With no saved profile, `cdb` asks for the URL, authentication kind, user name
and password, and offers to save the profile. The password goes into the OS
keychain, never into the config file.

### Logging in

There is deliberately no `--password` flag. Three ways to authenticate:

1. Guided setup, recommended the first time. With no profile saved, run
   `cdb connect` with no arguments. It prompts for the URL, the
   authentication kind (press Enter for `session`), the username, and the
   password with echo off, verifies against the server, then asks whether
   to save the profile. The password goes into the OS keychain, never into
   `config.toml`.

2. One line, saved as a profile:

   ```
   cdb connect --save --as local http://admin:password@localhost:5984
   ```

   The username and password are split out of the URL and stored in the
   keychain under the profile name; the config file keeps only the URL and
   username. Afterwards `cdb connect local`, or just `cdb`, uses it, and the
   only profile becomes the default.

3. Environment variables, for scripts or a quick test:

   ```
   CDB_URL=http://localhost:5984 CDB_USER=admin CDB_PASSWORD=password cdb ls /
   ```

   These override any profile and never touch the keychain. `CDB_TOKEN` does
   the same for JWT.

A URL typed with no user name and password — `cdb connect
http://localhost:5984` — is asked about rather than assumed. A CouchDB with
an admin configured accepts an anonymous connection and then refuses every
command, so on a terminal `connect` asks for a username and password first;
press Enter at the password to connect anonymously anyway. Pass
`--anonymous` to skip the questions. Without a terminal there is nobody to
ask, so the connection is made anonymously and says so once on stderr. A
command that is then refused says what to do about it: *The server requires
credentials for … . Connect with a username and password, or set CDB_USER and
CDB_PASSWORD.*

Once a default profile exists, `cdb` alone opens the shell already connected,
and `cdb session` shows who you are logged in as. Profile names may not
contain a dot, so use `local` rather than a hostname.

```
$ cdb
cdb> connect http://admin:password@localhost:5984 --save --as local
Connected to CouchDB 3.5.2 at localhost:5984 as admin.
admin@localhost:5984:/> ls
┌───────────┬──────┬──────────┬─────────────┐
│ NAME      │ DOCS │     SIZE │ PARTITIONED │
├───────────┼──────┼──────────┼─────────────┤
│ movies    │ 4211 │ 12.4 MB  │ false       │
│ _users    │    1 │ 16.3 KB  │ false       │
└───────────┴──────┴──────────┴─────────────┘
admin@localhost:5984:/> cd movies
admin@localhost:5984:/movies> ls --limit 3 --fields title,year
admin@localhost:5984:/movies> cat tt0111161
admin@localhost:5984:/movies> find '{"year":{"$gt":2000}}' --fields title,year --limit 5
admin@localhost:5984:/movies> cat tt0111161 | .title
"The Shawshank Redemption"
```

Press Tab at any point to complete command names, flags, database names,
document ids, view names, and document field names sampled from the database.
`history` lists the lines you have run; repeated and unparseable lines are not
recorded, and any credential in a URL you typed is stripped before the line is
stored. Ctrl-C abandons the running command and returns to the prompt; Ctrl-D
exits.

The history file is `$XDG_STATE_HOME/cdb/history`, which is
`~/.local/state/cdb/history` on macOS and Linux and
`%LOCALAPPDATA%\cdb\history` on Windows. Delete it to forget everything you
have typed.

## One-shot use

Every shell command is also a subcommand, so `cdb` works in pipelines. When
stdout is not a terminal the output is compact JSON, one document per line.

```
cdb ls / --json | jq -r '.name'
cdb find movies '{"year":2001}' --fields title | jq -r '.title'
echo '{"title":"Amélie","year":2001}' | cdb put movies/tt0211915
cdb backup movies movies.cdb.gz
cdb restore movies.cdb.gz movies_restored --create
cdb replicate movies https://backup.example.com/movies --continuous
cdb replications
```

`put <path>` reads standard input when there is no file argument, which is what
a pipeline wants. When running interactively (the shell, or a one-shot command
at a terminal) standard input is the terminal itself, so there `put` needs
either a file or an explicit `-`.

Exit codes: `0` success, `1` command error, `2` usage error, `3` connection or
authentication error, `130` interrupted.

### Global flags

Every one-shot subcommand accepts these, before or after its own flags:

| Flag | What it does | In the shell |
|---|---|---|
| `--profile <name>` | connect with a saved profile instead of the default | use `connect <name>` |
| `--url <url>` | connect with a server URL instead of a profile | use `connect <url>` |
| `--anonymous` | connect without credentials, and do not ask for any | yes |
| `--json` | raw JSON, one document per line, as when piped | yes |
| `--yes` | skip confirmation prompts | yes |
| `--verbose` | append the raw status, error name and server reason to errors | yes |
| `--path <path>` | start at a virtual path, so `cdb --path /movies ls` lists that database | use `cd` |
| `--format table\|json` | output format on a terminal | no |
| `--color auto\|always\|never` | ANSI colour; `NO_COLOR` overrides it | no |
| `--pager <cmd>` | pager command, or `off` | no |

Inside the shell only the four marked "yes" are accepted on a line; the rest
are one-shot flags, and their shell equivalents are the commands named above.
`--format`, `--color` and `--pager` have no per-line form there and are read
from `config.toml` for the whole session.

## Configuration

`$XDG_CONFIG_HOME/cdb/config.toml`, which is `~/.config/cdb/config.toml` on
macOS and Linux and `%APPDATA%\cdb\config.toml` on Windows:

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

Profile names may not contain a dot. Secrets live in the OS keychain — macOS
Keychain, Windows Credential Manager, Secret Service or KWallet on Linux, or
an encrypted file under the config directory when none of those is available
— under service `cdb`, keyed by profile name. They are never written to
`config.toml` and never accepted as flags.

For scripts and CI, these environment variables override the file and skip
the keychain entirely: `CDB_PROFILE`, `CDB_URL`, `CDB_USER`, `CDB_PASSWORD`,
`CDB_TOKEN`, `CDB_INSECURE_TLS`. An explicit `--profile` or `--url` on the
command line wins over `CDB_PROFILE`/`CDB_URL`, which in turn win over the
config file.

Two more control the keychain itself: `CDB_KEYRING_BACKEND` forces one
backend (`file` is what CI uses), and `CDB_KEYRING_PASSPHRASE` supplies the
encrypted file backend's passphrase, which otherwise can only be typed at a
terminal.

Fetched attachment files (`cdb fetch`) are created with permissions `0600`.

## Shell completion

```
cdb completion bash   > /etc/bash_completion.d/cdb
cdb completion zsh    > "${fpath[1]}/_cdb"
cdb completion fish   > ~/.config/fish/completions/cdb.fish
cdb completion powershell | Out-String | Invoke-Expression
```

## Commands

| Command | What it does |
|---|---|
| `connect`, `profiles`, `session` | Connect, manage saved profiles, show who you are |
| `cd`, `pwd`, `ls`, `info` | Move around and list databases, documents, views |
| `cat`, `put`, `rm`, `edit` | Read and write documents |
| `find`, `query` | Mango queries and map/reduce views |
| `tail` | Watch a database's changes feed |
| `mkdir`, `rmdir`, `cp` | Create and delete databases, copy documents |
| `attach`, `fetch` | Upload and download attachments, streamed |
| `conflicts`, `resolve` | Find and resolve conflicting revisions |
| `backup`, `restore` | Attachment-safe dump and load, resumable |
| `replicate`, `replications` | Start and watch replications |
| `help`, `history`, `clear`, `exit` | Shell only |

Destructive commands (`rm`, `rmdir`, `resolve`, `replications cancel`) ask
before acting. Pass `--yes` to skip the prompt in scripts. `rmdir` also asks you
to retype the database name.

### Watching changes

```
cdb tail /mydb                       # the last 25 changes
cdb tail /mydb --since 941-g1AAA…    # continue from a sequence
cdb tail /mydb --follow              # keep reading; Ctrl-C to stop
cdb tail /mydb --follow --json | jq  # one change per line, unbuffered
```

`tail` reads one page and stops unless `--follow` is given, in which case it
opens CouchDB's continuous feed at the database's current sequence and keeps
reading. A dropped feed is reconnected from the last change it showed you,
backing off 1s, 2s, 4s, 8s, 16s and then every 30s, and saying so on stderr
each time; a deleted database or a rejected token ends the command instead, and
so does a single change larger than 4 MiB, which no reconnect could get past —
re-run without `--include-docs`. `--include-docs` adds the changed document,
`--filter ddoc/name` applies a design-document filter, and `--heartbeat` (with
`--follow`) sets how often the server sends a keep-alive.

CouchDB has no partition-scoped changes feed, so `tail` takes a database path.

### Replication and same-server jobs

`replicate` and `cp` between two databases on the same server hand CouchDB the
URL `cdb` itself connected with (CouchDB rejects a bare database name). If the
server cannot reach that address from where it runs — a remapped Docker port,
an SSH tunnel — the job is still accepted, and then fails with `econnrefused`;
check it with `replications show <id>`. Credentials for these jobs are stored
in CouchDB's own `_replicator` database, as CouchDB requires.

### Backup and restore

`backup` writes a snapshot of the live documents and their attachments. It
carries no deletion tombstones, so a restored database does not know which
documents were deleted in the source — use replication, not backup/restore, to
keep two databases in step. A conflicted document is dumped once per leaf
revision, so the document count in the dump's footer counts revisions, not
distinct documents.

`restore` recreates any conflicts from the dump. Attachments up to 4 MiB
restore inline, preserving the exact revision from the dump; larger
attachments are uploaded in a separate step afterwards, which bumps the
revision — `restore` names which documents changed revision when this
happens.

## Development

```
go test ./...                                    # unit tests
docker run -d --name cdb-test -e COUCHDB_USER=admin -e COUCHDB_PASSWORD=password -p 15984:5984 couchdb:3.5
curl -X PUT http://admin:password@localhost:15984/_users
curl -X PUT http://admin:password@localhost:15984/_replicator
CDB_TEST_URL=http://localhost:15984/ go test ./... # plus integration tests
```

CI pins `CDB_KEYRING_BACKEND=file` so tests never touch a real OS keychain.

## License

Apache-2.0.
