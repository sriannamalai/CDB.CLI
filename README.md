# cdb

`cdb` is a single-binary command line client for Apache CouchDB 3.0 through 3.5.
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

**Proxy authentication.** For a CouchDB configured with
`proxy_authentication_handler`, where an upstream proxy — here, `cdb` itself —
asserts who the user is:

```
cdb connect --auth proxy --roles _admin https://couch.example.com
```

It asks for the user name, the roles to claim, and the shared secret from the
server's `[chttpd_auth] secret`, with echo off; the secret goes into the OS
keychain like any password. `cdb` sends `X-Auth-CouchDB-UserName`,
`X-Auth-CouchDB-Roles` and an `X-Auth-CouchDB-Token` that is an HMAC of the
user name keyed by that secret, and the same three headers are written into any
replication job it starts. CouchDB 3.3.2 and later verify that HMAC as SHA-256;
3.0 through 3.3.1 verify SHA-1 only, so `connect` probes the server once and
pins the answer as the profile's `proxy_hash` — that is what a `proxy_hash =
"sha1"` in your `config.toml` means. A wrong secret does not fail at the server —
CouchDB just treats the request as anonymous — so `connect` checks
`GET /_session` and refuses unless it reports `proxy` for the name you gave.
`CDB_PASSWORD` supplies the shared secret for a saved proxy profile in a
script.

**IBM Cloudant.** For Cloudant, authenticate with an IAM API key:

```
cdb connect --auth iam https://<account>.cloudantnosqldb.appdomain.cloud
CDB_IAM_API_KEY=… cdb ls /              # the same thing, for a script
```

`cdb` exchanges the key for a bearer token at
`https://iam.cloud.ibm.com/identity/token`, sends it on every request, and
refreshes it before it expires — an IAM token lasts an hour, so a long backup
or a `tail` outlives one and is refreshed under it. Set `iam_url` in the
profile, or `CDB_IAM_URL`, to use a different token endpoint. The API key is
the profile's keychain secret; no token ever reaches `config.toml` or your
shell history. A replication job started under an IAM profile carries the key
in the job document's own `auth.iam.api_key`, which Cloudant accepts natively,
so a continuous job keeps working without `cdb` refreshing anything for it.

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

Stages chain with `|`, mixing commands and jq expressions; a value that names a
document flows from a reference-producing command like `find` into `put`, `rm`
or `cat`:

```
admin@localhost:5984:/movies> find '{}' | select(.year > 2000) | put /recent
```

See `help pipelines` for the full grammar, plus variables and scripts.

Partitioned databases are addressed with a `_partition` segment:

```
cdb ls   /movies/_partition/2024                          # documents in the partition
cdb cat  /movies/_partition/2024/shawshank                # the document 2024:shawshank
cdb find /movies/_partition/2024 '{"rating":{"$gt":9}}'   # a partitioned Mango query
cdb query /movies/_partition/2024/_design/app/_view/by_date
```

A document in partition `2024` has the id `2024:<rest>`, so
`/movies/_partition/2024/shawshank` and `/movies/_partition/2024/2024:shawshank`
name the same document. Design documents are not partition-scoped: read one at
`/movies/_design/app`, and add `/_view/<name>` under a partition to run its view
against that partition. `ls --start` inside a partition takes the fully
qualified id, `2024:m`. `cd ..` out of a partition lands on the database.

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
| `--replication-url <url>` | address the server should use to reach itself for replication | yes |
| `--anonymous` | connect without credentials, and do not ask for any | yes |
| `--json` | raw JSON, one document per line, as when piped | yes |
| `--yes` | skip confirmation prompts | yes |
| `--verbose` | append the raw status, error name and server reason to errors | yes |
| `--path <path>` | start at a virtual path, so `cdb --path /movies ls` lists that database | use `cd` |
| `--format table\|json` | output format on a terminal | no |
| `--color auto\|always\|never` | ANSI colour; `NO_COLOR` overrides it | no |
| `--pager <cmd>` | pager command, or `off` | no |

Inside the shell only the five marked "yes" are accepted on a line; the rest
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
auth = "session"          # session | jwt (3.1+) | none
username = "admin"
insecure_tls = false
ca_file = ""
replication_url = "http://couchdb:5984"   # optional; defaults to url

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
the keychain entirely: `CDB_PROFILE`, `CDB_URL`, `CDB_REPLICATION_URL`,
`CDB_USER`, `CDB_PASSWORD`, `CDB_TOKEN`, `CDB_INSECURE_TLS`. An explicit
`--profile`, `--url` or `--replication-url` on the command line wins over
`CDB_PROFILE`/`CDB_URL`/`CDB_REPLICATION_URL`, which in turn win over the
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
| `search` | Full-text queries against Clouseau and Nouveau indexes |
| `tail` | Watch a database's changes feed |
| `mkdir`, `rmdir`, `cp` | Create and delete databases, copy documents |
| `attach`, `fetch` | Upload and download attachments, streamed |
| `conflicts`, `resolve` | Find and resolve conflicting revisions |
| `backup`, `restore` | Attachment-safe dump and load, resumable |
| `replicate`, `replications` | Start and watch replications |
| `tasks`, `config`, `users`, `security`, `compact`, `cluster` | Administer the server: active tasks, configuration, accounts, database security, compaction, single-node setup |
| `help`, `history`, `clear`, `exit` | Shell only |
| `set`, `unset`, `run` | Shell only: variables and scripts; see `help pipelines` |

Destructive commands (`rm`, `rmdir`, `resolve`, `replications cancel`, `users rm`,
`config unset`, `compact`, `cluster setup`) ask before acting. Pass `--yes` to skip the prompt in scripts. `rmdir` also asks you
to retype the database name. `resolve` shows how each conflicting revision
differs from the current one before it asks which to keep; `--diff-full` prints
each revision in full instead.

### Full-text search

`search` runs a Lucene query against a search index defined in a design
document. CouchDB has two search backends and the path says which one:
`_search` for a Clouseau index (defined under `indexes`) and `_nouveau` for a
Nouveau one (defined under `nouveau`).

```
cdb search /movies/_design/app/_search/by_title 'title:arrival'
cdb search /movies/_design/app/_nouveau/by_body 'alien AND linguist' --include-docs
cdb search /movies/_design/app/_search/by_title 'title:a*' --limit 10 --counts genre
cdb search /movies/_partition/2024/_design/app/_search/by_title 'title:a*'
cdb info   /movies/_design/app/_nouveau/by_body
```

`ls` on a design document lists its search indexes beside its views, with the
backend in its own column, and Tab completes `_search`, `_nouveau` and the
index names under either. Paging is by bookmark, the way `find` pages: a full
page prints the exact command that fetches the next one. `--sort` and
`--ranges` are passed to the server as typed, because their grammar is the
backend's.

Neither backend runs inside CouchDB itself — both are separate services an
operator has to deploy. If neither is running, `cdb` says which one is missing
rather than repeating the server's own wording.

### Watching changes

```
cdb tail /mydb                       # the first page of changes
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

`replicate` and `cp` between two databases on the same server hand CouchDB a
full URL to dial, because CouchDB rejects a bare database name. By default that
is the URL `cdb` itself connected with, which is wrong whenever the two do not
agree — a container published on `localhost:15984` that calls itself
`http://couchdb:5984`, an SSH tunnel, a reverse proxy; the job is accepted and
then fails with `econnrefused`, which `replications show <id>` reports.

Set `--replication-url http://couchdb:5984` (or `replication_url` in the
profile, or `CDB_REPLICATION_URL`) to the address the server knows itself by.
It is a server address, not a database one, and it must not carry a user name
or password: `cdb` sends the profile's credentials as an Authorization header
on the endpoint instead, where the header is stored in the `_replicator`
database as CouchDB requires. `info /` shows the value when it differs from the
connected URL; `profiles list` does not show it, so its columns stay stable for
scripts.

### Backup and restore

`backup` writes a snapshot of the live documents and their attachments. Pass
`--tombstones` to record deletions too: a deleted document is dumped as an
ordinary record carrying `_deleted: true` and its full revision history, so a
restore reproduces the deletion and replicates it onward. Without the flag the
dump holds live documents only, as in 1.0, and a restored database does not
know which documents were deleted in the source. A dump is either a record of
deletions or it is not — `--resume` refuses to continue one in the other mode.
A conflicted document is dumped once per leaf revision, so the document count
in the dump's footer counts revisions, not distinct documents; the footer's
`deleted` count says how many of those were tombstones.

`restore` recreates any conflicts from the dump. Attachments up to 4 MiB
restore inline, preserving the exact revision from the dump; larger
attachments are uploaded in a separate step afterwards, which bumps the
revision — `restore` names which documents changed revision when this
happens.

### Administration

| Command | What it does |
| --- | --- |
| `cdb tasks` | What the server is working on right now: replications, compactions, index builds. `--watch` keeps it up to date. |
| `cdb config` | Read and change `_node/<node>/_config`. Credentials print as `****` unless you pass `--reveal`, and a setting CouchDB only reads at start-up says so. |
| `cdb users` | List, add, remove and re-password both `_users` accounts and `[admins]` server admins. Passwords are prompted or read from stdin, never typed on the command line. |
| `cdb security` | Show or edit a database's `_security` document: who may read it, who may administer it. |
| `cdb compact` | Compact a database or a design document's views, optionally following it to the end with `--watch`. |
| `cdb cluster` | Report the cluster state and node membership, and configure a fresh server as a single node. |

Multi-node cluster setup (`enable_cluster`, `add_node`, `finish_cluster`) is
not supported; use Fauxton or `curl` for that.

## Development

```
go test ./...                                    # unit tests
docker run -d --name cdb-test -e COUCHDB_USER=admin -e COUCHDB_PASSWORD=password -p 15984:5984 couchdb:3.5
curl -X PUT http://admin:password@localhost:15984/_users
curl -X PUT http://admin:password@localhost:15984/_replicator
CDB_TEST_URL=http://localhost:15984/ go test ./... # plus integration tests
```

The replication round-trip test needs the server to be able to dial itself: the
container above publishes 5984 as 15984, so set
`CDB_TEST_REPLICATION_URL=http://localhost:5984/` alongside `CDB_TEST_URL`. CI
needs neither, because its service container reaches itself at the same address.

The JWT integration test needs CouchDB 3.1 or later — the JWT handler does not
exist before 3.1 — with the handler enabled, which is
not CouchDB's default. Configure one and restart it — the handler list is read
at start-up and never re-read:

```
b64=$(printf %s 'dev-jwt-secret' | base64 | tr -d '\n')
curl -X PUT -H 'Content-Type: application/json' \
  "http://admin:password@localhost:15984/_node/_local/_config/jwt_keys/hmac:_default" -d "\"$b64\""
curl -X PUT -H 'Content-Type: application/json' \
  "http://admin:password@localhost:15984/_node/_local/_config/chttpd/authentication_handlers" \
  -d '"{chttpd_auth, jwt_authentication_handler}, {chttpd_auth, cookie_authentication_handler}, {chttpd_auth, default_authentication_handler}"'
docker restart cdb-test
CDB_TEST_URL=http://localhost:15984/ CDB_TEST_JWT_SECRET=dev-jwt-secret go test ./internal/command/
```

Keeping the cookie and default handlers means username-and-password auth keeps
working alongside the token. The test skips unless both `CDB_TEST_URL` and
`CDB_TEST_JWT_SECRET` are set.

The proxy integration test needs a server with
`proxy_authentication_handler` in its chain and a shared secret, which is not
CouchDB's default either. Which config section holds the secret is a version
question: 3.3.2 and later read `[chttpd_auth]`, 3.0 through 3.3.1 read
`[couch_httpd_auth]`, so write both and the same command works on any supported
server. The handler list is read at start-up, so restart afterwards:

```
for section in chttpd_auth couch_httpd_auth; do
  curl -X PUT "http://admin:password@localhost:15984/_node/_local/_config/$section/secret" -d '"proxysecret"'
  curl -X PUT "http://admin:password@localhost:15984/_node/_local/_config/$section/proxy_use_secret" -d '"true"'
done
curl -X PUT "http://admin:password@localhost:15984/_node/_local/_config/chttpd/authentication_handlers" \
  -d '"{chttpd_auth, proxy_authentication_handler}, {chttpd_auth, cookie_authentication_handler}, {chttpd_auth, jwt_authentication_handler}, {chttpd_auth, default_authentication_handler}"'
docker restart cdb-test
CDB_TEST_URL=http://localhost:15984/ CDB_TEST_PROXY_SECRET=proxysecret go test ./internal/command/
```

The test skips unless both `CDB_TEST_URL` and `CDB_TEST_PROXY_SECRET` are set.
There is no version gate: the handler exists on every supported server. Which
digest it verifies differs — 3.3.2 and later take HMAC-SHA256 or HMAC-SHA1, 3.0
through 3.3.1 only SHA-1 — and cdb finds that out for itself, saving the answer as
the profile's `proxy_hash`.

The search integration test needs a server with a full-text backend, which
stock CouchDB has neither of. Nouveau is the one that can be run from images:
a `couchdb:3.5-nouveau` container for the index service and a `couchdb:3.5`
beside it pointed at it.

```
docker network create cdbnet
docker run -d --name cdb-test-nv --network cdbnet couchdb:3.5-nouveau
docker run -d --name cdb-test-35nv --network cdbnet \
  -e COUCHDB_USER=admin -e COUCHDB_PASSWORD=password -p 15987:5984 couchdb:3.5
for kv in enable:true url:http://cdb-test-nv:5987; do
  curl -X PUT "http://admin:password@localhost:15987/_node/_local/_config/nouveau/${kv%%:*}" \
    -d "\"${kv#*:}\""
done
docker restart cdb-test-35nv
CDB_TEST_URL=http://localhost:15987/ CDB_TEST_NOUVEAU=1 go test ./internal/command/
```

`CDB_TEST_NOUVEAU` is the gate rather than the URL alone, because a plain 3.5
answers 404 for every `_nouveau` path and the test would assert nothing. Left
unset, the companion test instead checks the two "no backend here" sentences
against the plain server, which is what an operator without either backend
sees.

There is no Clouseau container, so Clouseau's response shape is covered by
fixtures rather than a live server.

The Cloudant IAM tests need a real Cloudant instance and are never run in CI.
Set `CDB_TEST_CLOUDANT_URL` and `CDB_TEST_CLOUDANT_API_KEY` from your own
credentials; both tests skip when either is unset.

`CDB_TEST_FRESH_URL` names a CouchDB the test suite may **reconfigure**: the
single-node cluster-setup test posts to `_cluster_setup` and creates the
system databases. Leave it unset — it is unset in CI — unless you have started
a throwaway server for it:

```
docker run -d --name cdb-test-fresh -p 15988:5984 \
  -e COUCHDB_USER=admin -e COUCHDB_PASSWORD=password couchdb:3.5
CDB_TEST_FRESH_URL=http://localhost:15988/ go test ./internal/command/ -run ClusterSetup
docker rm -f cdb-test-fresh
```

`CDB_TEST_FRESH_USER` and `CDB_TEST_FRESH_PASSWORD` override the credentials,
which default to `admin`/`password`.

CI pins `CDB_KEYRING_BACKEND=file` so tests never touch a real OS keychain, and
runs `gofmt -l .`, `go vet ./...` and `GOOS=windows go vet ./...` as gates.

## License

Apache-2.0.
