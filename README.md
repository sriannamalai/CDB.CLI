# cdb

`cdb` is a single-binary command line client for Apache CouchDB 3.2 through 3.5.
It is an interactive shell with a filesystem metaphor and a set of one-shot
subcommands for scripts.

## Install

Homebrew:

```
brew install sriannamalai/tap/cdb
```

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

## Quickstart

`cdb` starts the interactive shell only when both stdin and stdout are a
terminal; otherwise it runs the one-shot subcommand named on the command line.
With no saved profile, `cdb` asks for the URL, authentication kind, user name
and password, and offers to save the profile. The password goes into the OS
keychain, never into the config file.

```
$ cdb
cdb> connect http://localhost:5984
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

To save a connection you opened by URL, add `--save` (and `--as <name>` to
choose the profile name): `connect http://localhost:5984 --save --as local`.
Profile names may not contain a dot.

Press Tab at any point to complete command names, flags, database names,
document ids, view names, and document field names sampled from the database.
`history` lists the lines you have run; repeated and unparseable lines are not
recorded. Ctrl-C abandons the running command and returns to the prompt; Ctrl-D
exits.

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

Exit codes: `0` success, `1` command error, `2` usage error, `3` connection or
authentication error, `130` interrupted.

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
`CDB_TOKEN`, `CDB_INSECURE_TLS`.

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
| `mkdir`, `rmdir`, `cp` | Create and delete databases, copy documents |
| `attach`, `fetch` | Upload and download attachments, streamed |
| `conflicts`, `resolve` | Find and resolve conflicting revisions |
| `backup`, `restore` | Attachment-safe dump and load, resumable |
| `replicate`, `replications` | Start and watch replications |
| `help`, `history`, `clear`, `exit` | Shell only |

Destructive commands (`rm`, `rmdir`, `resolve`, `replications cancel`) ask
before acting. Pass `--yes` to skip the prompt in scripts. `rmdir` also asks you
to retype the database name.

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
