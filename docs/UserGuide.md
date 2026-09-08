# cdb user guide

`cdb` is a single-binary client for Apache CouchDB 3.2 through 3.5: an
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
never from the table: CouchDB accepts a bare number in `--since` and answers it
by replaying the feed from the beginning.

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
find it again. Credentials in an endpoint URL are moved into CouchDB's
per-endpoint auth object, so they never reach the stored document's URL or the
screen — though they do live in the server's own `_replicator` database, as
CouchDB requires.

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
sends the profile's credentials in CouchDB's per-endpoint auth object instead.
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
recorded, nor is a repeat of the previous line, and any credential in a URL
you typed is stripped before the line reaches the history file — delete that
file to forget everything. A trailing `| <jq expression>` filters the
command's JSON result before it is rendered; one stage, real jq syntax:

```
admin@localhost:5984:/> cat /movies/tt2543164 | .title
"Arrival"
```

A line ending in a backslash continues on the next line, and so does one with
an unclosed quote or an unbalanced `{` or `[` — which is what makes a long
Mango selector typeable. Ctrl-C abandons the running command and returns to
the prompt; Ctrl-D at an empty prompt exits, as do `exit` and `quit`; `clear`
clears the screen. Inside the shell only `--json`, `--yes`, `--verbose`,
`--anonymous` and `--replication-url` may be typed on a line, each applying to
that line alone: `--profile` and `--url` have `connect` as their equivalent,
`--path` has `cd`, and `--format`, `--color` and `--pager` are read from
`config.toml` for the whole session.

## Scripting

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
| `CDB_TOKEN` | JWT, selecting `jwt` auth; wins over `CDB_PASSWORD` |
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
| no credentials sent | `The server requires credentials for list databases. Connect with a username and password, or set CDB_USER and CDB_PASSWORD.` |
| not allowed | `You do not have permission to read document "doc1" in "mydb".` |
| no such database | `Database "mydb" does not exist. "ls /" lists databases.` |
| no such document | `Document "doc1" was not found in "mydb".` |
| someone else wrote first | `Document "doc1" was changed by someone else. "cat doc1" shows the latest version.` |
| server down or wrong port | `Could not reach localhost:5984. Is CouchDB running?` |

`--verbose` appends the raw status, the CouchDB error name and the server's
reason — `… [status 401 unauthorized: You are not a server admin.]`. Declining
a confirmation prints `Cancelled: nothing was changed.` and exits `1`; nothing
was sent to the server.

## Troubleshooting

**"Login failed for … Check the password."** The username reached the server
and the password was rejected. Re-run `cdb connect <profile>` to retype it, or
`cdb profiles list` to see which URL and username that profile holds. If you
supply `CDB_PASSWORD`, remember it overrides the keychain.

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

Beyond this guide: the [command reference](reference/README.md), `help` inside
the shell, and `cdb <command> --help` outside it.
