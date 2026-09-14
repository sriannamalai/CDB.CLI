# cdb user guide

`cdb` is a single-binary client for Apache CouchDB 3.0 through 3.5: an
interactive shell that treats a server as a filesystem, plus the same commands
as one-shot subcommands for scripts. This guide is task-shaped; for the exact
flags and argument rules of one command read its page in the
[command reference](reference/README.md), generated from the binary itself.

## Installing

Homebrew:

```
brew tap sriannamalai/tap
brew trust sriannamalai/tap
brew install --cask cdb
```

Homebrew 6 refuses to load casks from a third-party tap until it is trusted;
`brew trust` records that in `~/.homebrew/trust.json`. Skip it and the install
fails with an untrusted-tap error. Scoop, on Windows:

```
scoop bucket add sriannamalai https://github.com/sriannamalai/scoop-bucket
scoop install cdb
```

From source, with Go 1.27 or newer: `go install
github.com/sriannamalai/CDB.CLI/cmd/cdb@latest`. Otherwise download an archive
from the [releases page](https://github.com/sriannamalai/CDB.CLI/releases),
verify it against the release's `checksums.txt`, and put `cdb` on your `PATH`.
Check the install with `cdb version`, which prints
`cdb 1.1.0 (commit 0b6f3a1, built 2026-09-08T10:12:00Z)` — and `unknown` for
any field not injected at build time, which is what a source build looks
like.

## Connecting

**Guided setup.** With no profile saved and a terminal on both ends,
`cdb connect` with no argument asks for the server URL, the authentication
kind (press Enter for `session`), the username, and the password with echo
off. It verifies the answers before writing anything, then offers to save
them: the password to the OS keychain, and only the URL, auth kind and
username to `config.toml`. There is deliberately no `--password` flag, so a
password never reaches your shell history or the process list.

**Profiles.** A profile is a named server. Save one in a line:

```
$ cdb connect --save --as local http://admin:password@localhost:5984/
Connected to CouchDB 3.5.2 at localhost:5984 as admin. Saved as profile "local".
```

The credentials are split out of the URL. `profiles` manages them afterwards:

```
$ cdb profiles list
 NAME  | URL                    | AUTH    | DEFAULT
-------+------------------------+---------+---------
 local | http://localhost:5984/ | session | yes
$ cdb profiles add prod https://admin:secret@couch.example.com/
$ cdb profiles default prod
$ cdb profiles remove prod
```

`profiles add` saves a server without connecting to it, and a URL typed with
no user name and password is asked about rather than saved as it stands: on a
terminal it prompts for the user name and the password with echo off, proves
them against the server, and puts the password in the keychain. A profile with
neither could not log in, so `cdb connect` on it would accept every command and
then refuse it. Press Enter at the password, or pass `--anonymous`, to save a
profile that connects anonymously; in a script, where there is nobody to ask,
nothing is prompted and the URL is stored as typed.

```
$ cdb profiles add prod https://couch.example.com/
This server may need a login. Press Enter at the password to connect anonymously.
Username [admin]: admin
Password:
Saved profile "prod" for https://couch.example.com/. Run "cdb connect prod" to use it.
```

Profile names may not contain a dot: name one `prod`, not
`couch.example.com`. Four ways to choose one, in decreasing precedence:

| How | Scope |
|---|---|
| `cdb --profile prod ls /`, `cdb --url http://host:5984 ls /` | one command |
| `CDB_PROFILE=prod cdb ls /`, `CDB_URL=…` | one process |
| `connect prod` inside the shell | the rest of the session |
| `cdb profiles default prod` | every later run |

The rule is **flags beat environment variables beat the config file**. For
scripts and CI the variables skip the keychain entirely: `CDB_URL`,
`CDB_REPLICATION_URL`, `CDB_USER`, `CDB_PASSWORD`, `CDB_TOKEN` (JWT) and
`CDB_INSECURE_TLS`.

**Anonymous connections.** A URL typed with no username and password is asked
about rather than assumed: a CouchDB with an admin accepts an anonymous
connection and then refuses every command, so on a terminal `connect` asks for
credentials first. Press Enter at the password prompt to connect anonymously
anyway, or pass `--anonymous` to skip the questions; without a terminal there
is nobody to ask, so the connection is anonymous and says so once on stderr.
Every command accepts `--anonymous`, because any command can be the one that
opens the connection.

**Where secrets live.** Under the service name `cdb`, keyed by profile name,
in the first of these that works: macOS Keychain, Windows Credential Manager,
Secret Service, KWallet, then an encrypted file at `<config dir>/cdb/keyring`.
They never reach `config.toml`, are never accepted as a flag, and are never
echoed. `CDB_KEYRING_BACKEND` forces one backend by name (`file` is what CI
uses); `CDB_KEYRING_PASSPHRASE` supplies the file backend's passphrase, which
otherwise can only be typed at a terminal.

| What | macOS and Linux | Windows |
|---|---|---|
| `config.toml` | `$XDG_CONFIG_HOME/cdb/`, else `~/.config/cdb/` | `%APPDATA%\cdb\` |
| shell history | `$XDG_STATE_HOME/cdb/`, else `~/.local/state/cdb/` | `%LOCALAPPDATA%\cdb\` |

**Proxy authentication.** `connect --auth proxy` is for a CouchDB that has
been put behind a trusted front end and configured with
`proxy_authentication_handler`, with `cdb` itself standing in as that front
end. It asks for the user name, an optional comma-separated list of roles
(blank means a real user with none, not "unknown"), and a shared secret; the
secret is kept in the OS keychain exactly like a password, never in
`config.toml`, and `--roles` on the command line or `roles` in the profile
sets the roles outside the prompt. On every request `cdb` sends the user
name, the roles, and an `X-Auth-CouchDB-Token` header it computes itself, an
HMAC of the user name keyed by the shared secret. CouchDB 3.3.2 and later
verify that HMAC as SHA-256; CouchDB 3.0 through 3.3.1 expect SHA-1, and
`connect` probes the server once to find out which, then pins the answer in
the profile's `proxy_hash` so later commands do not have to probe again. The
secret itself lives under `[chttpd_auth] secret` on 3.3.2 and later and under
`[couch_httpd_auth] secret` on 3.0 through 3.3.1; either way, the handler
that reads it is named in `[chttpd] authentication_handlers`, and CouchDB
only reads that list at start-up, so enabling it needs a restart. A secret
that disagrees with the server's does not come back as an error: the server
just treats the request as anonymous, so `connect` checks `GET /_session`
itself and refuses to save or use a profile whose session is not a `proxy`
session for the user it asked for. Replication jobs started under a proxy
profile carry the same three headers, so a job started against a CouchDB 3.0
server works the same way it would against 3.5.

**IBM Cloudant with an IAM API key.** `connect --auth iam`, or the
environment variable `CDB_IAM_API_KEY`, authenticates to Cloudant with an IBM
Cloud IAM API key instead of a CouchDB user name and password. The key is
asked for once and kept in the keychain like a password; `cdb` exchanges it
for a bearer token before the first request and exchanges it again, in the
request that needs it, once less than a minute of the token's one-hour
lifetime remains -- and once more if the server answers 401 anyway -- so a
long-running command or an open shell session does not fail partway
through.
`CDB_IAM_URL`, or `iam_url` in the profile, points the exchange at a token
endpoint other than IBM's public one; the exchange itself is subject to the
same `ca_file` and `insecure_tls` profile settings as every other request, so
it also works behind a TLS-inspecting proxy. Neither the API key nor the
bearer token it becomes is ever written to `config.toml`, logged, or printed
in an error message, verbose or not. Cloudant accepts the API key natively
when `cdb` writes it into a replication document's own `auth.iam.api_key`, so
a continuous job keeps working without `cdb` having to refresh anything on
its behalf; `replicate` still only writes the job document, and does not
create the `_replicator` database itself, so one must already exist on the
Cloudant instance before the first job is started there.

## The virtual filesystem

Every path names something on the connected server:

| Path | Target |
|---|---|
| `/` | the server |
| `/mydb` | a database |
| `/mydb/doc1` | a document |
| `/mydb/_design/app` | a design document |
| `/mydb/_design/app/_view/by_date` | a view |
| `/mydb/doc1/photo.jpg` | an attachment |
| `/mydb/_partition/p1` | a partition, listing the documents in it |
| `/mydb/_partition/p1/doc1` | a document in that partition, whose id is `p1:doc1` |
| `/mydb/_partition/p1/doc1/photo.jpg` | an attachment on that document |
| `/mydb/_partition/p1/_design/app/_view/by_date` | a view, run against that partition |

A path starting with `/` is absolute; anything else is relative to the current
path, and `.` and `..` work. A `/` inside a database name or a document id —
CouchDB allows both — must be percent-encoded exactly as in the HTTP API: the
database `team/logs` is `/team%2Flogs`.

`cd` moves, `pwd` prints where you are, and the prompt follows along:
`admin@localhost:5984:/movies> `. `ls` shows databases at `/`, documents inside a database, and the views,
updates and filters inside a design document, one page at a time:

```
$ cdb ls /movies --limit 2
 ID          | REV
-------------+------------------------------------
 _design/app | 2-38f0b8babb35aaf96420b30d54bd4ca1
 tt0211915   | 1-fe587ae7ef952dbac249a78f49bb51e6
more documents: ls /movies --start "tt0245429"
```

`--start <id>` begins the next page at that id, `--limit N` sets the page
size, `--fields a,b` adds document fields as columns, and `--all` streams every
row instead of one page — for a pipe or a file rather than your eyes. `info`
prints a key-value table for a database, or for the server at `/`:

```
$ cdb info /movies
 FIELD       | VALUE
-------------+-------------
 name        | movies
 documents   | 2
 disk size   | 24.4 KB
 partitioned | false
```

`mkdir /mydb` creates a database (`--partitioned`, `--q N` for the shard
count); `rmdir /mydb` deletes one. `rmdir` is destructive: on a terminal it
makes you retype the database name, and without one it refuses unless `--yes`
is given.

**Partitions.** A partitioned database — one made with `mkdir --partitioned`,
and the `partitioned` row of `info` says which — gives every document an id of
the form `<key>:<rest>`. The `_partition` segment addresses one key, and
everything under it may be written with the short id, so
`/movies/_partition/2024/shawshank` and
`/movies/_partition/2024/2024:shawshank` name the same document:

```
cdb ls    /movies/_partition/2024                          # documents in the partition
cdb cat   /movies/_partition/2024/shawshank                # the document 2024:shawshank
cdb cat   /movies/_partition/2024/shawshank/poster.jpg     # an attachment on it
cdb find  /movies/_partition/2024 '{"rating":{"$gt":9}}'   # a partitioned Mango query
cdb query /movies/_partition/2024/_design/app/_view/by_date
```

`cat`, `put`, `rm`, `edit`, `attach` and `fetch` all take a partitioned path,
and `find` and `query` run partitioned queries, which read that partition
alone. Design documents are not partition-scoped: read one at
`/movies/_design/app`, and add `/_view/<name>` under a partition to run its
view against that partition. `ls --start` inside a partition takes the fully
qualified id, `2024:m`, because that is what the previous page printed.

`_partition` is a separator, not a directory of its own — `/movies/_partition`
names nothing — so stepping up steps over it. From
`/movies/_partition/2024/shawshank`, `..` is the partition, `../..` is
`/movies`, and `../../other` is the document `other` in the database. One `..`
followed by a name is the sideways step you would expect: `cd ../2025` from
inside partition `2024` lands on the sibling partition.

## Working with documents

`cat` prints a document as JSON — highlighted on a terminal, compact when
piped:

```
$ cdb cat /movies/tt0211915
{
  "_id": "tt0211915",
  "_rev": "1-fe587ae7ef952dbac249a78f49bb51e6",
  "title": "Amelie",
  "year": 2001
}
```

`--rev <rev>` reads one revision, `--revs` includes the revision history, and
`--conflicts` includes the conflicting revisions of a conflicted document; on
an attachment path, `cat` writes its bytes to stdout. `put` creates or updates
a document from a file or from standard input:

```
$ echo '{"title":"Amelie","year":2001}' | cdb put /movies/tt0211915
Wrote /movies/tt0211915 at revision 1-fe587ae7ef952dbac249a78f49bb51e6.
$ cdb put /movies/_design/app ddoc.json
```

If the input carries a `_rev`, that revision is used; if not and the document
exists, `put` fetches the current revision and asks before overwriting. Run
interactively — in the shell, or one-shot at a terminal — `put` needs a file
argument or an explicit `-`, because reading the terminal to end-of-file would
look like a hang; piped, no argument is needed.

`edit` is the round trip: it fetches the document, opens it in `$EDITOR`
(falling back to `$VISUAL`), validates the JSON on save, and writes it back
with the revision it fetched. A quoted editor path with spaces works:
`EDITOR='"/Applications/My Editor/bin/ed" -w'`. If someone else wrote first,
`edit` reloads and asks whether to reapply your edits or discard them. `rm`
deletes a document or an attachment, and is destructive:

```
$ cdb rm /movies/tt0211915-copy --yes
Deleted /movies/tt0211915-copy. The tombstone revision is 2-42584260d2245a1e54d90e26b349154c.
```

`cp` copies a document with a CouchDB `COPY` request and, when both sides are
databases, starts a one-shot replication instead —
`cdb cp /movies/tt0211915 /movies/tt0211915-copy`, or
`cdb cp /movies /movies-archive`.

## Querying

`find` runs a Mango `_find`:

```
$ cdb find /movies '{"year":{"$gt":2010}}' --fields title,year
 ID | DOCUMENT
----+---------------------------------
    | {"title":"Arrival","year":2016}
```

With no selector on a terminal, `find` walks a guided builder: a field, an
operator (`=`, `!=`, `<`, `<=`, `>`, `>=`, `in`, `exists`), a value, then
whether to add another condition. It prints the selector it built before
running it, so you can lift that into a script.

The flags are `--fields`, `--sort field:asc` or `field:desc`, `--limit` (25 by
default), `--bookmark` and `--use-index`; `--explain` prints CouchDB's query
plan instead of running the query, which is how you check that an index is
used. Sorting on an unindexed field fails with *The server rejected the
request: No index exists for this sort* — create the index, or drop `--sort`.
Paging is by bookmark, and a full page prints the next line for you:

```
more documents: find /movies --bookmark "g2wAAAACaAJkAA5zdGFydGtleV9kb2NpZG0…"
```

CouchDB returns a bookmark on every query whether or not more documents exist,
so a full page is the only signal of a next one. `query` runs a view:

```
$ cdb query /movies/_design/app/_view/by_year --limit 3
 KEY  | ID        | VALUE
------+-----------+-----------------
 2001 | tt0211915 | "Amelie"
 2016 | tt2543164 | "Arrival"
```

Its flags are `--key`, `--startkey` and `--endkey` (each a JSON value),
`--descending`, `--reduce`, `--group-level`, `--include-docs` and `--limit`.
`--reduce` and `--group-level` are sent only when you set them, so a view with
a reduce function is reduced by CouchDB's own default; pass `--reduce=false`
for the map rows. Paging is by key:

```
more rows: query /movies/_design/app/_view/by_year --startkey "2016"
```

`search` runs a full-text query against a Clouseau or Nouveau index. The path
names the backend as its second-to-last segment, `_search` for Clouseau or
`_nouveau` for Nouveau, with the index name last:

```
$ cdb search /movies/_design/app/_search/by_title 'title:arrival'
 ID | SCORE | FIELDS
----+-------+---------------------------
 m1 | 1.25  | {"title":"Arrival"}

$ cdb search /movies/_design/app/_nouveau/by_body 'alien AND linguist' --include-docs
```

A partitioned database takes the partition as a third path segment ahead of
`_design`, the same way `query` does. The query itself is Lucene syntax, sent
to the server exactly as typed; `--sort` and `--ranges` are passed through
verbatim too, because their grammar belongs to whichever backend answers.
Paging is by bookmark, the same shape as `find`'s:

```
$ cdb search /movies/_design/app/_search/by_title 'title:a*' --limit 10
more results: search /movies/_design/app/_search/by_title "title:a*" --bookmark "g1AAAAF9eJ…"
```

Under `--json` the same bookmark rides in the trailing object as `bookmark`,
so a script can page without parsing the hint.

`--counts field` (repeatable) returns facet counts for a field, `--ranges`
takes a JSON object of named ranges, and `--drilldown field:value`
(repeatable) restricts to a facet value. The counts and the ranges the server
returns are printed as a hint line alongside the rows, and ride in the JSON
tail under `--json`; a drilldown only narrows the query. `info` on an index path — the same command used for a
database or a view — reports the index's own statistics instead of running a
query:

```
$ cdb info /movies/_design/app/_search/by_title
```

## Watching changes

`tail` reads a database's `_changes` feed, newest changes last:

```
$ cdb tail /movies --limit 3
 SEQ | ID        | REV                                | DELETED
-----+-----------+------------------------------------+---------
 3-… | tt0211915 | 1-967a00dff5e02add41819138abb3284d | false
 4-… | tt2543164 | 2-7051cbe5c8faecd085a3fa619e6e6337 | false
 5-… | tt0245429 | 3-825cb35de44c433bfb2df415563a19de | true
more changes: tail /movies --since "5-g1AAAAFV…"
Sequences are shortened in the table; use --json for the full value and --since.
```

Without `--follow` it reads one page — 25 changes by default, and `--limit 0`
reads to the end of the feed — and stops. The page starts at the beginning of
the feed unless `--since <seq>` continues it from a sequence you already hold,
so a database with a long history takes a `--since` or a `--follow` to reach
what happened recently. `--include-docs` adds the changed document to each row,
and `--filter ddoc/name` applies a design-document filter; both halves are
required, so `--filter app/` is a usage error rather than a request the server
refuses.

**Sequences are shortened in the table.** A CouchDB update sequence is a long
opaque string, so the table prints only its leading number and an ellipsis,
which is the part worth reading. The whole value is what `--json` prints and
what `--since` takes, so copy it from `--json` or from the paging hint and
never from the table: CouchDB rejects a bare number in `--since` as a malformed
sequence.

**`--follow`** opens CouchDB's continuous feed at the database's current
sequence and keeps reading until you stop it with Ctrl-C, printing each change
the moment it arrives instead of collecting a page first:

```
$ cdb tail /movies --follow
$ cdb tail /movies --follow --json | jq -r '.id'
```

A dropped feed is reopened from the last change it showed you, backing off 1s,
2s, 4s, 8s, 16s and then every 30s, and saying so on stderr each time — so a
redirected stdout collects the changes and nothing else. A deleted database or
a rejected token ends the command instead, and so does a single change larger
than 4 MiB, which no reconnect could ever get past: re-run without
`--include-docs`. `--heartbeat <ms>` sets how often the server sends a
keep-alive on an idle feed and applies only with `--follow`, as `--limit`
applies only without it.

CouchDB has no partition-scoped changes feed, so `tail` takes a database path.

## Attachments

`attach` uploads a file, streamed; `fetch` downloads one:

```
$ cdb attach /movies/tt0211915 ./poster.txt
Attached poster.txt (17 B) to /movies/tt0211915. The document is now at revision 5-ac866d9c221230c601e098ecae4e1c4d.
$ cdb fetch /movies/tt0211915/poster.txt
Wrote poster.txt (17 B) from /movies/tt0211915/poster.txt.
```

`attach --name` sets the attachment name when it should differ from the file's
base name, and `--content-type` overrides the type guessed from the extension.
`fetch` writes to the attachment's own name in the current directory unless
given an output path, refuses to overwrite an existing file unless `--force`
is given, and creates the file with permissions `0600`; `--rev` reads from a
specific revision, and `fetch <path> -` streams to stdout. To delete an
attachment, `rm` its path.

## Conflicts

`conflicts` on a database lists documents with conflicting revisions; on a
document it shows those revisions side by side.

```
$ cdb conflicts /movies
 ID         | REV                                | CONFLICTS
------------+------------------------------------+-----------
 conflicted | 1-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb |         2
resolve one with: resolve <path>
```

`--limit` sets how many documents are scanned per page. `resolve` keeps one
revision and deletes the rest. In a script you name the survivor with
`--keep`:

```
$ cdb resolve /movies/conflicted --keep 1-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb --yes
Kept revision 1-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb of /movies/conflicted and deleted 2 other revision(s).
```

On a terminal it asks instead, and describes each revision by what differs
from the current one, field by field:

```
admin@localhost:5984:/> resolve /movies/conflicted
1) 1-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb (current)
   no differences from the current revision
2) 1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa (conflict)
   changed: year 1994 → 1995; added: rating 9.3; removed: draft
Keep which revision? [1-2]
```

Only top-level fields are compared, and CouchDB's own `_`-prefixed bookkeeping
is left out of the comparison — except `_deleted`, which is the whole content
of a tombstone, and `_attachments`, which is compared by attachment count
rather than by its stubs' digests. Key order and whitespace do not register.
A value too long for the line is shortened with an ellipsis for display only:
two revisions that differ past the ellipsis are still reported as different.
`--diff-full` prints each revision as indented JSON instead, for when the
summaries are not enough; it shows the revisions before you choose one, so it
has no effect with `--keep` and saying both is a usage error.

CouchDB picks a deterministic winner among conflicting revisions, and that is
what `cat` shows. Keeping a non-winner is a real edit: the winner is deleted
along with the other conflicts, so the document's content changes for everyone
and the deletion replicates like any other write. Read the revisions first.

## Backup and restore

`backup` writes a database and its attachments to one gzip-compressed file of
newline-delimited JSON records: a header, one `doc` record per revision, an
`att` record followed by the attachment's raw bytes, a `checkpoint` after each
batch, and a footer with the totals.

```
$ cdb backup /movies movies.cdb.gz
Wrote 5 document(s), 1 attachment(s) and 1.1 KB from "movies" to movies.cdb.gz (sequence 7-g1AAAACLeJzLYWBgYM…).
```

`--batch N` sets the documents per checkpoint; `--resume` reads the last
checkpoint in an existing file and continues from there, appending, which
makes an interrupted dump cheap to finish; `--since <seq>` starts from an
update sequence you already hold. Two things to know before relying on a dump:

- **Deletions are recorded only if you ask.** By default a dump carries live
  documents only, so a restored database does not know what was deleted in the
  source. `--tombstones` records them too: a deleted document is dumped as an
  ordinary record carrying `_deleted: true` and its full revision history, so
  a restore reproduces the deletion, and replicates it onward like any other
  write. A dump is either a record of deletions or it is not — `--resume`
  refuses to continue one in the other mode, and says which it is.
- **Records are per leaf revision.** A conflicted document is dumped once per
  conflicting revision, so the footer's document count counts revisions, not
  distinct documents; the footer's deletion count says how many of those were
  tombstones.

```
$ cdb backup /movies movies.cdb.gz --tombstones
Wrote 6 document(s) (1 of them deletions), 1 attachment(s) and 1.3 KB from "movies" to movies.cdb.gz (sequence 8-g1AAAACLeJzLYWBgYM…).
```

`restore` streams the file back:

```
$ cdb restore movies.cdb.gz /movies-copy --create
Restored 5 document(s), 1 attachment(s) and 1.1 KB into "movies-copy".
```

`--create` creates the target, partitioned if the dump's header says the
source was; `restore` refuses a target that already holds documents unless
`--merge` is given; `--batch N` sets the documents per bulk write.
`--partial` accepts a dump with no footer — what an interrupted `backup`
leaves behind — so that a footerless dump is otherwise an error rather than a
silent partial restore, and a complete dump's footer counts are compared with
what was loaded, failing and naming both numbers on a mismatch. The deletion
count is checked the same way.

Revisions and conflicts are preserved, and so are tombstones: a `_deleted`
record goes back through `_bulk_docs` like any other, so the target ends with
the same deletions rather than with those documents resurrected, and `restore`
counts them for you — *Restored 6 document(s) (1 of them deletions),
1 attachment(s) and 1.3 KB into "movies-copy".* A dump written without `--tombstones` restores exactly as
it did before, knowing nothing about what the source deleted.

Attachments up to 4 MiB are restored inline with their document, which keeps
the exact revision the dump recorded; anything larger is uploaded afterwards,
which bumps the revision, and `restore` names the documents that changed
revision for that reason.

## Replication

`replicate` writes a job to the server's `_replicator` database:

```
$ cdb replicate /movies /movies-backup --create-target --id movies-job
Started replication movies-job from http://localhost:5984/movies to http://localhost:5984/movies-backup. Run "cdb replications" to watch it.
```

Each endpoint is a database path on the connected server (`/movies`) or a full
`http(s)` URL. `--continuous` keeps the job running as changes arrive,
`--create-target` creates a missing target, `--filter ddoc/name` applies a
design-document filter, and `--id` names the `_replicator` document so you can
find it again. Credentials in an endpoint URL are sent as an Authorization
header on the endpoint instead, so they never reach the stored document's URL
or the screen — though the header does live in the server's own
`_replicator` database, as CouchDB requires.

```
$ cdb replications
$ cdb replications show movies-job
$ cdb replications cancel movies-job --yes
```

`replications list` reads `_scheduler/docs`, one row per replication document;
`show` joins the `_scheduler/jobs` entry, adding the start time, the process
id and the scheduler history — a job that has finished, failed or not started
has no scheduler entry, and `show` prints the document alone. `--watch`
redraws every two seconds until interrupted. `cancel` deletes the
`_replicator` document, which stops the job; it confirms first, and without a
terminal it refuses unless `--yes` is given.

**The same-server caveat.** CouchDB 3.x rejects a bare database name as an
endpoint (`local_endpoints_not_supported`), so a same-server job —
`replicate /a /b`, or `cp` between two databases — has to be handed a full URL
to dial. By default that is the URL `cdb` itself connected with, which is
wrong whenever the two do not agree: a container published with
`-p 15984:5984` answers you on `localhost:15984` and knows itself as
`http://couchdb:5984`, and an SSH tunnel or a reverse proxy is the same story.
The job is accepted and then fails with `econnrefused`, which
`replications show <id>` reports.

`--replication-url` names the address the *server* knows itself by. It is a
global flag, so every command accepts it; `replicate` and `cp` are the ones
that write it into the job:

```
$ cdb replicate /movies /movies-backup --replication-url http://couchdb:5984
```

It is a server address, not a database one, and it must carry no user name or
password: `cdb` refuses one that does — without echoing what you typed — and
sends the profile's credentials as an Authorization header on the endpoint
instead.
Set it once as `replication_url` in the profile, or as `CDB_REPLICATION_URL` in
the environment; the rule is the one the server URL follows, **flag beats
environment variable beats the config file**. In the shell `--replication-url`
may be typed on any line, and applies to that line only. `info /` shows the
value the next replication would use, and only when it differs from the
connected URL:

```
$ cdb info / --replication-url http://couchdb:5984
 FIELD           | VALUE
-----------------+------------------------
 url             | http://localhost:15984
 replication url | http://couchdb:5984
 version         | 3.5.2
```

`profiles list` does not show it, so its columns stay stable for scripts.

## Administration

These six commands cover what Fauxton's admin pages do. All of them need
server administrator rights; without them, every one answers

    Server administrator rights are required for changing configuration.

with the action named, and exits 1.

### What the server is doing

`cdb tasks` lists `_active_tasks`: one row per running replication, database
or view compaction, and index build.

    $ cdb tasks
     TYPE                | PROGRESS | TARGET | STARTED  | UPDATED  | NODE
    ---------------------+----------+--------+----------+----------+---------------
     database_compaction | 84%      | movies | 15:20:00 | 15:20:31 | nonode@nohost

`--type` keeps one kind of task, `--db /movies` keeps the tasks for one
database (a replication counts if either of its endpoints is that database),
and `--watch` re-reads the list every two seconds — `--interval 5s` to slow it
down — printing the rows again whenever the set of tasks changes. A task that
has finished is simply gone; `tasks` is a snapshot, not a history.

### Configuration

`cdb config` reads `_node/<node>/_config`. With no argument it prints
everything, with a section name that section, and with `section/key` one
value:

    $ cdb config log/level
     SECTION | KEY   | VALUE
    ---------+-------+-------
     log     | level | info

**Credentials are redacted.** Everything under `[admins]` and `[jwt_keys]`,
the shared proxy secret in `[chttpd_auth] secret` and `[couch_httpd_auth]
secret`, and any key whose name ends in `password`, `secret` or `token`
prints as `****`. `--reveal` prints the real value, and the same rule applies
to `--json`, so a redirected `cdb config --json > config.json` does not leave
a secret on disk by accident.

`cdb config set section/key value` writes one setting and reports what it
replaced; `cdb config unset section/key` removes it and always asks first.

**Some settings are read only when CouchDB starts.** The ports and bind
addresses, `[chttpd] authentication_handlers`, the data directories,
`[couchdb] uuid` and `single_node`, `[cluster] n` and `q`, every JWT key,
everything under `[ssl]`, and the Nouveau switches. Changing one of those
changes what the *next* start will use and nothing about the running server,
so `config set` asks first and then says so, and `config unset` adds the
same sentence when it removes one:

    $ cdb config set chttpd/port 5985
    Change chttpd/port on _local? [y/N] y
    Set chttpd/port (was "5984"). This setting is read at start-up; restart CouchDB for it to take effect.

`cdb config reload` makes the node re-read its `.ini` files, which discards
any change made through this command that was never written to disk. It is
not a substitute for a restart on the settings above.

`--node` names the node; it defaults to `_local`, CouchDB's alias for
whichever node answered.

### Accounts

CouchDB has two kinds of account and `cdb users` manages both. An ordinary
user is a document in the `_users` database; a **server admin** is a key in
the `[admins]` configuration section and may do anything on the server.

    $ cdb users
     NAME  | KIND         | ROLES
    -------+--------------+----------------
     admin | server admin |
     alice | user         | editor, reader

    $ cdb users add alice --roles editor,reader
    Password for alice:
    Repeat password for alice:
    Created user "alice".

`--admin` makes the account a server admin instead. `cdb users passwd <name>`
changes a password, `cdb users rm <name>` removes an account, and
`cdb users show <name>` prints the account's fields — never the password hash,
only whether one is set.

**A password is never a command-line argument**, where it would reach the
shell history and every process list on the machine. cdb prompts for it twice
with the echo off, or reads it as a single line on standard input:

    printf '%s\n' "$NEW_PASSWORD" | cdb users passwd alice --password-stdin

`cdb users add` creates the `_users` database if the server has none yet, and
`cdb users rm` refuses to remove the account you are connected as.

**A server admin's password is hashed asynchronously.** After `users add
--admin` or `users passwd` on a server admin, CouchDB's PUT to `[admins]`
returns before the password is actually hashed — hashing takes roughly
100–200 ms. cdb waits for it, polling the config entry for up to two seconds,
so that a login attempted right after the command returns succeeds instead of
being refused against the still-plaintext value. On a very slow server that
two-second wait can itself run out; cdb still reports success, since the
write did land, but a login attempted immediately afterwards can be refused
once — retry it.

### Who may read a database

`cdb security /movies` prints the database's `_security` document, one entry
per row, and the eight `--add-*`/`--remove-*` flags edit it:

    $ cdb security /movies --add-member carol --remove-member bob
    Grant member access on movies to carol, revoke member access on movies from bob? [y/N] y
    Changed the security of movies.

Every flag in one call is applied in a single `PUT` and confirmed by a single
question, and members of the document cdb does not manage are left alone.

### Reclaiming space

`cdb compact /movies` starts a compaction: CouchDB rewrites the database file
without the superseded revisions of its documents. It is safe — the database
stays readable and writable — but it is long-running on a large database and
needs room for a second copy of the file while it runs, so cdb asks first.

`--ddoc by_year` compacts that design document's view indexes instead of the
database. `--cleanup` additionally deletes the index files left behind by
design documents that have changed or gone. `--watch` follows the matching
`_active_tasks` entry to the end:

    $ cdb compact /movies --watch --yes
    type                 progress  target  started   updated   node
    database_compaction  12%       movies  15:20:00  15:20:02  nonode@nohost
    database_compaction  86%       movies  15:20:00  15:20:14  nonode@nohost
                                   Compaction of movies finished.

A small database compacts faster than the first poll and never produces a task
at all; a watch that has seen nothing after ten seconds reports the compaction
as finished, because it is.

### Cluster state and single-node setup

`cdb cluster status` reports what `_cluster_setup` and `_membership` say,
plus the `[cluster] n` and `q` of the node `--node` names:

    $ cdb cluster status
     FIELD            | VALUE
    ------------------+------------------
     state            | cluster_finished
     cluster n        | 3
     cluster q        | 2
     all_nodes[0]     | node1@127.0.0.1
     cluster_nodes[0] | node1@127.0.0.1

A server whose setup endpoint is switched off reports the state as
`unavailable`; the node rows still work.

`cdb cluster setup --single-node` turns a fresh server into a working
single-node install: CouchDB sets `[cluster] n` to 1 and creates the `_users`
and `_replicator` databases. A node that is already set up is reported and
left alone. **Multi-node setup is not supported** — `enable_cluster`,
`add_node` and `finish_cluster` are not offered, and a real cluster is still
built with Fauxton or `curl`.

## The interactive shell

`cdb` with no subcommand starts the shell when both stdin and stdout are a
terminal. The prompt is `user@host:/current/path> `, or `cdb> ` when nothing
is connected. Tab completes command names, flags, database names, document
ids, view names, and document field names for `find --fields` and `--sort`:
database names come from `_all_dbs`, cached for the session and refreshed
after `mkdir` and `rmdir`; document ids from a prefix query on `_all_docs`;
field names from a sample of up to 50 documents, cached per database.

`help` lists every command with a `!` against the destructive ones, and
`help <command>` explains one with its flags and a worked example:

```
admin@localhost:5984:/> help ls
ls [path]
  List databases, documents, or the parts of a design document
  aliases: list
```

`history` numbers the lines you have run. A line that failed to parse is not
recorded, nor is a line whose first word names no command, nor a repeat of the
previous line, and any credential in a URL you typed is stripped before the
line reaches the history file — delete that
file to forget everything. A trailing `| <jq expression>` filters the
command's JSON result before it is rendered; one stage, real jq syntax:

```
admin@localhost:5984:/> cat /movies/tt2543164 | .title
"Arrival"
```

A line ending in a backslash continues on the next line, and so does one with
an unclosed quote or an unbalanced `{` or `[` — which is what makes a long
Mango selector typeable. A backslash elsewhere escapes the next character, so
a Windows path goes in single quotes (`run 'C:\scripts\nightly.cdb'`) or uses
forward slashes. Ctrl-C abandons the running command and returns to
the prompt; Ctrl-D at an empty prompt exits, as do `exit` and `quit`; `clear`
clears the screen. Inside the shell only `--json`, `--yes`, `--verbose`,
`--anonymous` and `--replication-url` may be typed on a line, each applying to
that line alone: `--profile` and `--url` have `connect` as their equivalent,
`--path` has `cd`, and `--format`, `--color` and `--pager` are read from
`config.toml` for the whole session.

## Pipelines and scripts

A shell line is one or more stages separated by `|`. The first stage is a
command; every later stage is a command when its first word names one, and a jq
expression otherwise.

```
admin@localhost:5984:/movies> ls | .id
admin@localhost:5984:/movies> find '{"year":{"$gt":2000}}' | put /old
admin@localhost:5984:/movies> ls | cat | .title
```

Copying needs no `del(._rev)` stage: `put` drops an incoming `_rev` itself and
writes over whatever revision the target holds.

Values flow from stage to stage as JSON, one at a time, and the stages run at
the same time. A feed with no end therefore keeps working:

```
admin@localhost:5984:/movies> tail --follow --include-docs | .doc | put /audit
```

and a slow stage makes the one above it wait rather than filling memory.

### What a stage may be

Three commands read a pipeline. `help <command>` says which, and so does each
command's reference page.

| Stage | Reads | What it does |
|---|---|---|
| `put [<db>]` | documents | Writes each value, which must be a JSON object, through `_bulk_docs` in batches of 100. An incoming `_rev` is dropped: `put` looks up each batch's ids in the target database first and writes over the current revision when one exists, so a pipeline copies and updates without conflicting and without asking to confirm the overwrite. One row per document: `id`, `rev`, `status`. A document the server refuses is a row with its error name as the status — `conflict`, say — not a failed line. |
| `rm [<db>]` | references | Confirms once (`Delete the piped documents from <db>?`), then deletes in batches. A document that is not there is a row with the status `not_found`. |
| `cat [<db>]` | references | Fetches each document named and emits it. An id the database does not hold ends the stage: `"tt0211915" is not in movies`. |

A **reference** is what `ls`, `find`, `query`, `search`, `tail` and `cat`
produce: a document id, an object carrying `_id` or `id`, or an absolute
`/db/id` path, which overrides the stage's own database. The database may be
left out when the current directory is inside one; at `/` the answer is
`put needs a database path when the current directory is /`.

Piping into a command that reads no pipeline is a mistake, not a silent drop:
`mkdir does not read a pipeline.`

A command that changes the session rather than reading or writing
data — `connect`, `profiles`, `cd`, `exit`, `clear`, `history`, `help`, `run`,
`set` and `unset` — cannot start a line of more than one stage:
`cd cannot start a pipeline.` A line of one stage is unaffected; `cd /movies`
is the command it has always been.

### When a line fails

The failing stage stops the others, and the sentence says which stage it was:

```
admin@localhost:5984:/movies> ls | put /nowhere
stage 2 (put): The database nowhere does not exist.
```

The exit code is the one that failure would have had on its own. Ctrl-C cancels
the whole line: the shell prints nothing and returns to the prompt, and a script
or a one-shot run exits 130.

`--json`, `--yes`, `--verbose`, `--anonymous` and `--replication-url` are read
from the first stage and apply to the whole line.

### Variables

```
admin@localhost:5984:/movies> set year 2001
admin@localhost:5984:/movies> find --field year | select(.year > $year)
admin@localhost:5984:/movies> set rev = cat tt0211915 | ._rev
admin@localhost:5984:/movies> set
 NAME | VALUE
------+------------------------------------
 rev  | 1-fe587ae7ef952dbac249a78f49bb51e6
 year | 2001
```

`$name` and `${name}` are replaced inside a word of a command stage, and the
word is never re-split — a value holding a space stays one argument. They are
never replaced inside a word any part of which is single-quoted, which is how
a Mango selector or a jq expression keeps a `$` of its own, a backslash
protects a lone `$` the same way (`\$a`), and `$$` is a literal `$`. A `$`
right after a `"` is left alone, so Mango operators such as `"$gt"` need no
escaping. In a jq
stage the variables are not text-replaced at all: each is bound as a jq
variable of the same name with its stored value, so a number is a number.

`set <name> = <pipeline>` stores what the pipeline produced: one value as that
value, several as a JSON array. Everything after the `=` is the pipeline, taken
exactly as it was typed — a `|` in it belongs to the pipeline, not to the `set`
line — so nothing needs quoting. `unset <name>` removes a variable; unsetting one
that was never set is not an error.

A variable whose name ends in `password`, `secret` or `token` prints as `****`
in `set`'s listing and is recorded in the history the same way. Nothing is
persisted: variables live as long as the shell does.

### Script files

```
$ cdb run nightly.cdb
$ cdb run --yes purge.cdb movies 2001
$ cdb < nightly.cdb
admin@localhost:5984:/> run nightly.cdb
```

A script is a file of the same lines the shell reads. A `#` that begins a
bare, unquoted word starts a comment that runs to the end of its line — the
same rule the shell itself uses, so a note after a command
(`ls /movies # spot check`) and a whole line of comment are both written the
same way. Blank lines are skipped, and a line continues onto the next the way
it does in the shell. The `.cdb` extension is a convention, not a rule.

A script is never interactive, whatever terminals the process has: no prompt and
no guided builder is reachable while it runs, so a line that would confirm needs
`--yes` — on the line, or on `run` itself, which is read before the file name
(`cdb run --yes purge.cdb movies 2001`; everything after the file name reaches
the script as `$1`, `$2`, …). Execution stops at the first failing line:

```
$ cdb run nightly.cdb
nightly.cdb:4: The database nowhere does not exist.
$ echo $?
1
```

A line too long to read is reported the same way, `<file>:<line>: the line is
longer than 1048576 bytes.` A line beginning with `-` has its failure reported
and then ignored. `exit` ends the script with success. Arguments after the
file name are `$1` to `$9`, and `$#` is how many there are. A script gets its
own variable scope, so it never changes its caller's variables, while
`connect` and `cd` lines take effect for the rest of it — and, when it was run
from the shell, for the shell afterwards. Scripts nest eight deep.

```
# nightly.cdb — archive last year's films
cd /movies
set rev = cat tt0211915 | ._rev
find '{"year":{"$lt":2000}}' | put /archive
ls | rm --yes
```

`help pipelines` prints all of this in the shell.

## Scripting

This chapter is about calling `cdb` from an outside shell script. For cdb's own
pipelines, variables and script files — `cdb run job.cdb`, `cdb < job.cdb` —
read [Pipelines and scripts](#pipelines-and-scripts) above.

Every shell command is also a subcommand. When stdout is not a terminal the
output is compact JSON, one document per line, with no colour and no pager:

```
cdb ls / --json | jq -r '.name'
cdb find movies '{"year":2001}' --fields title | jq -r '.title'
echo '{"title":"Amelie","year":2001}' | cdb put movies/tt0211915
cdb backup movies movies.cdb.gz
```

`--json` forces that same output on a terminal; `--format table|json` chooses
between the table and pretty-printed JSON when there *is* a terminal;
`--color auto|always|never` controls ANSI colour, with `NO_COLOR` in the
environment overriding it; `--pager <cmd>` sets the pager, or `--pager off`
turns it off; `--path /movies` starts a one-shot command at a virtual path, so
`cdb --path /movies ls` lists that database; and `--replication-url` names the
address the server should use to reach itself, as
[Replication](#replication) explains.

| Exit code | Meaning |
|---|---|
| `0` | success |
| `1` | command error — the server refused, or the command declined to act |
| `2` | usage error — unknown command, bad flag, wrong argument count |
| `3` | connection or authentication error |
| `130` | interrupted with Ctrl-C |

| Variable | Effect |
|---|---|
| `CDB_PROFILE` | profile to connect with, unless `--profile` is given |
| `CDB_URL` | server URL, unless `--url` is given |
| `CDB_REPLICATION_URL` | address the server should use to reach itself for replication, unless `--replication-url` is given |
| `CDB_USER` | username |
| `CDB_PASSWORD` | password, selecting `session` auth |
| `CDB_TOKEN` | JWT (CouchDB 3.1 or later), selecting `jwt` auth; wins over `CDB_PASSWORD` |
| `CDB_INSECURE_TLS` | skip TLS certificate verification when true |
| `CDB_KEYRING_BACKEND` | force one keyring backend, e.g. `file` |
| `CDB_KEYRING_PASSPHRASE` | passphrase for the encrypted file backend |
| `EDITOR`, `VISUAL` | the editor `edit` opens |
| `NO_COLOR` | disable colour, overriding `--color` |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME` | where the config and history live |

Completion scripts for the one-shot subcommands:

```
cdb completion bash   > /etc/bash_completion.d/cdb
cdb completion zsh    > "${fpath[1]}/_cdb"
cdb completion fish   > ~/.config/fish/completions/cdb.fish
cdb completion powershell | Out-String | Invoke-Expression
```

## Errors

A failure is one sentence that names what to do next, not a status code:

| What happened | What you see |
|---|---|
| bad password | `Login failed for admin at localhost:5984. Check the password with "profiles" or "connect".` |
| rejected token | `The server rejected the token.` |
| rejected token, server too old | `The server rejected the token. JWT authentication needs CouchDB 3.1 or later; this server is 3.0.1.` |
| no credentials sent | `The server requires credentials for list databases. Connect with a username and password, or set CDB_USER and CDB_PASSWORD.` |
| not allowed | `You do not have permission to read document "doc1" in "mydb".` |
| no such database | `Database "mydb" does not exist. "ls /" lists databases.` |
| no such document | `Document "doc1" was not found in "mydb".` |
| someone else wrote first | `Document "doc1" was changed by someone else. "cat doc1" shows the latest version.` |
| server down or wrong port | `Could not reach localhost:5984. Is CouchDB running?` |

`--verbose` appends the raw status, the CouchDB error name and the server's
reason — `… [status 401 unauthorized: You are not a server admin.]`. Declining
a confirmation prints `Cancelled: nothing was changed.` and exits `1`; nothing
was sent to the server. Ctrl-C at that same prompt exits `130` in silence,
with no `Cancelled` line — same result, no server write, no message.

## Troubleshooting

**"Login failed for … Check the password."** The username reached the server
and the password was rejected. Re-run `cdb connect <profile>` to retype it, or
`cdb profiles list` to see which URL and username that profile holds. If you
supply `CDB_PASSWORD`, remember it overrides the keychain.

**"The server rejected the token."** The token itself was refused. On a server
older than 3.1 the message continues "JWT authentication needs CouchDB 3.1 or
later; this server is 3.0.1." — the JWT handler does not exist before 3.1, so
no token can work there; switch that profile to `session` auth. On 3.1 or
later, the token has likely expired or was signed with a key the server does
not have configured.

**"The server did not accept the proxy credentials for … . Check the shared
secret and that proxy authentication is enabled on the server."** Either the
secret disagrees with the server's `[chttpd_auth] secret`, or
`proxy_authentication_handler` is not in `[chttpd] authentication_handlers`.
`cdb` cannot tell the two apart: a proxy token CouchDB will not accept does not
produce an error, it produces an anonymous session, which is why `connect`
checks `GET /_session` rather than trusting the status code. Check the handler
chain with `curl .../_node/_local/_config/chttpd/authentication_handlers`, and
remember it is read at start-up only — a change needs a restart.

**"IBM IAM did not issue a token for the API key. Check the key."** Nothing
reached Cloudant at all: IBM refused the key. Re-run with `--verbose` to see
IBM's own message, which usually says whether the key is unknown, disabled, or
belongs to another account.

**"The server rejected the IAM token at … ."** A token was issued and Cloudant
refused it twice, once before and once after a refresh, so a stale token is not
the problem. The service id the key belongs to has no access to that instance,
or the URL names a different instance.

**"This server has no search service running …"** and **"Nouveau is not enabled
on this server …"** Neither search backend runs inside CouchDB: Clouseau and
Nouveau are separate services an operator deploys and wires up, and the stock
`couchdb` image enables neither. `_search` needs Clouseau; `_nouveau` needs the
Nouveau service and `[nouveau] enable = true` with `[nouveau] url` pointing at
it. `ls` on the design document shows which backend each index belongs to. The
sentence adds **"Nouveau needs CouchDB 3.4 or later; this server is …"** when
the server predates the endpoint, which is a different fix: an upgrade rather
than a configuration change.

**"Could not reach …. Is CouchDB running?"** Nothing answered on that host and
port. Check the port in `cdb profiles list` against the one the server is
published on — a container started with `-p 15984:5984` answers on 15984, not
5984 — and try `curl` against the same URL.

**"Cask is from an untrusted tap."** Homebrew 6 needs
`brew trust sriannamalai/tap` before `brew install --cask cdb`.

**Keyring failures on a headless Linux box.** With no Secret Service or
KWallet running, the keyring falls back to an encrypted file at
`<config dir>/cdb/keyring`, whose passphrase can only be typed at a terminal.
In CI or a container, set `CDB_KEYRING_BACKEND=file` and
`CDB_KEYRING_PASSPHRASE=…`, or skip stored secrets and use `CDB_URL`,
`CDB_USER` and `CDB_PASSWORD`, which never touch the keyring. If the keyring
cannot be opened at all, `cdb` says so and suggests those variables rather
than pretending to save a secret.

**A replication is accepted and then fails with `econnrefused`.** The server
cannot reach the address `cdb` connected with — the remapped-port and
SSH-tunnel case under [Replication](#replication). Re-run with
`--replication-url http://couchdb:5984`, naming the address the server knows
itself by, or set it once as `replication_url` in the profile or
`CDB_REPLICATION_URL` in the environment; `cdb info /` shows which value is in
force, and `cdb replications show <id>` reads back the error.

**`put` seems to hang at a terminal.** It does not: run interactively, `put`
asks for a file argument or an explicit `-` rather than reading the terminal
to end-of-file. Pass the file, or pipe the JSON in.

**`Server administrator rights are required for …`**
The account you are connected as is not a server admin. `_active_tasks`, the
configuration, `_users`, `_security`, compaction and cluster setup are all
admin-only. `cdb session` shows who you are connected as; `cdb users` lists
the server admins, and `cdb users add <name> --admin` creates one if you have
an admin account already.

**`config set` said the setting was changed and nothing happened**
The setting is one CouchDB reads only when it starts. `config set` says so in
the same breath; restart the server. `config reload` will not do it — it
re-reads the `.ini` files, which is a different thing.

**A user cannot log in after `users passwd`**
Check that nothing else wrote the `_users` document between the read and the
write. cdb strips the `password_sha`, `salt`, `derived_key`, `iterations` and
`password_scheme` fields before writing the new password, because CouchDB
ignores a plaintext `password` on a document that still carries the old hash;
a document edited by hand with `cdb put` can end up in that state.

**`cluster setup --single-node` says the node is already set up**
It is: `GET /_cluster_setup` reports `single_node_enabled` or
`cluster_finished`. Nothing was changed. `cdb cluster status` shows the state.

**`This node reports the setup state "…", which cdb does not know how to configure`**
cdb configures a node from `cluster_disabled`, `single_node_disabled` or
`cluster_enabled`. Any other state means someone has started a multi-node
setup, which cdb does not finish; use Fauxton or `curl`.

Beyond this guide: the [command reference](reference/README.md), `help` inside
the shell, and `cdb <command> --help` outside it.
