package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/backup"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// backupBatchSize is how many changes are read, and documents fetched, per
// checkpoint.
const backupBatchSize = 200

// Backup returns the backup command.
func Backup() Command {
	return Command{
		Name:    "backup",
		Summary: "Write a database, with attachments, to a dump file",
		Example: `$ cdb backup /movies movies.cdb.gz
5 documents, 1 attachments, 1.1 KB...
Wrote 5 document(s), 1 attachment(s) and 1.1 KB from "movies" to movies.cdb.gz (sequence 7-g1AAAACLeJzLYWBgYM...).

$ cdb backup /movies movies.cdb.gz --resume`,
		Usage:       "<db-path> <file>",
		MinArgs:     2,
		MaxArgs:     2,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("resume", false, "continue an interrupted dump")
			fs.String("since", "", "start from this update sequence")
			fs.Int("batch", backupBatchSize, "documents per checkpoint")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDatabase {
				return nil, Usagef("backup", "%s is %s %s; backup takes a database path", t.Path, t.Kind.Article(), t.Kind)
			}
			file := inv.Arg(1)
			batch := inv.Int("batch")
			if batch <= 0 {
				batch = backupBatchSize
			}

			since := inv.String("since")
			var docs, atts, written int64
			var f *os.File
			// needHeader stays true for a dump that starts from nothing. Scan
			// refuses a file whose first member is not a header, so a resume
			// that lands past offset 0 is resuming a dump that already has one.
			needHeader := true

			if inv.Bool("resume") {
				f, err = os.OpenFile(file, os.O_RDWR, 0o600)
				if err != nil {
					return nil, err
				}
				res, serr := backup.Scan(f)
				if serr != nil {
					f.Close()
					if errors.Is(serr, backup.ErrNotADump) {
						return nil, Usagef("backup", "%s is not a cdb dump, so --resume would overwrite it: pick another file, or remove --resume to start a new dump", file)
					}
					return nil, serr
				}
				if res.Complete {
					f.Close()
					return Message{Text: fmt.Sprintf("%s already contains a complete dump. Nothing to resume.", file)}, nil
				}
				// Scan leaves the file at an unspecified position, so cut the
				// partial trailing member by path and reopen for appending.
				f.Close()
				if err := backup.Truncate(file, res.Offset); err != nil {
					return nil, err
				}
				f, err = os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					return nil, err
				}
				if since == "" {
					since = res.Seq
				}
				needHeader = res.Offset == 0
				docs, atts, written = res.Docs, res.Attachments, res.Bytes
				fmt.Fprintf(s.Stderr, "Resuming %s from sequence %s (%d documents, %s already written).\n", file, sinceLabel(since), docs, humanBytes(written))
			} else {
				f, err = os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					if os.IsExist(err) {
						return nil, Usagef("backup", "%s already exists; pass --resume to continue it, or choose another file", file)
					}
					return nil, err
				}
			}
			defer f.Close()

			// The Writer must write straight through to the file: a buffer in
			// between would leave a checkpoint unwritten on disk, and an
			// interrupted dump would resume from a sequence it never stored.
			w := backup.NewWriter(f)
			if needHeader {
				info, err := s.Client.DatabaseInfo(ctx, t.Database)
				if err != nil {
					return nil, err
				}
				server, err := s.Client.ServerInfo(ctx)
				if err != nil {
					return nil, err
				}
				if err := w.WriteHeader(backup.Header{
					DB:          t.Database,
					Server:      server.Version,
					Started:     time.Now().UTC().Format(time.RFC3339),
					Partitioned: info.Partitioned,
				}); err != nil {
					return nil, err
				}
			}

			lastSeq := since
			// The progress line is rewritten in place with a carriage return,
			// so it has to be closed on every exit — including a Ctrl-C, whose
			// error would otherwise be printed onto the end of it.
			progressed := false
			defer func() {
				if progressed {
					fmt.Fprintln(s.Stderr)
				}
			}()
			for {
				page, err := s.Client.Changes(ctx, t.Database, lastSeq, batch)
				if err != nil {
					return nil, err
				}
				if len(page.Rows) == 0 {
					lastSeq = page.LastSeq
					break
				}
				refs := make([]couch.BulkRef, 0, len(page.Rows))
				for _, row := range page.Rows {
					// Tombstones are deliberately not carried: a dump holds
					// only live documents.
					if row.Deleted {
						continue
					}
					for _, rev := range row.Revs {
						refs = append(refs, couch.BulkRef{ID: row.ID, Rev: rev})
					}
				}
				bodies, failed, err := s.Client.BulkGet(ctx, t.Database, refs, true)
				if err != nil {
					return nil, err
				}
				// A revision the server cannot hand back — purged or compacted
				// away since the changes feed named it — is missing data. Skip
				// it and the footer would claim a completeness the dump does
				// not have, so stop instead and say which revision was lost.
				if len(failed) > 0 {
					e := failed[0]
					return nil, fmt.Errorf("backup: %d of %d revisions could not be fetched (first: %s@%s: %s); the dump is resumable from its last checkpoint",
						len(failed), len(refs), e.ID, e.Rev, e.Error)
				}
				for _, body := range bodies {
					stripped, attachments, id, rev, err := splitAttachments(body)
					if err != nil {
						return nil, err
					}
					// Any Writer error is terminal, so the dump is abandoned
					// here rather than continued past a desynchronised record.
					if err := w.WriteDoc(stripped); err != nil {
						return nil, err
					}
					docs++
					written += int64(len(stripped))
					for _, name := range attachments {
						n, err := writeAttachment(ctx, s, w, t.Database, id, rev, name)
						if err != nil {
							return nil, err
						}
						atts++
						written += n
					}
				}
				lastSeq = page.LastSeq
				// Checkpoint before the next batch, so a Ctrl-C leaves a dump
				// that resumes from here.
				if err := w.WriteCheckpoint(lastSeq); err != nil {
					return nil, err
				}
				// Spec section 9: progress carries document and byte counts.
				fmt.Fprintf(s.Stderr, "\r%d documents, %d attachments, %s…", docs, atts, humanBytes(written))
				progressed = true
				if page.Pending == 0 && len(page.Rows) < batch {
					break
				}
			}

			if err := w.WriteFooter(backup.Footer{Docs: docs, Attachments: atts, LastSeq: lastSeq}); err != nil {
				return nil, err
			}
			if err := w.Close(); err != nil {
				return nil, err
			}
			return Message{Text: fmt.Sprintf("Wrote %d document(s), %d attachment(s) and %s from %q to %s (sequence %s).", docs, atts, humanBytes(written), t.Database, file, lastSeq)}, nil
		},
	}
}

// sinceLabel renders a resume sequence for the operator. An empty sequence
// means the dump had no usable checkpoint and starts over from the beginning.
func sinceLabel(since string) string {
	if since == "" {
		return "the beginning"
	}
	return since
}

// splitAttachments removes the _attachments stubs from a document, because
// _bulk_docs with new_edits=false rejects them, and returns their names along
// with the document id and revision.
func splitAttachments(body json.RawMessage) (json.RawMessage, []string, string, string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, "", "", err
	}
	var id, rev string
	_ = json.Unmarshal(m["_id"], &id)
	_ = json.Unmarshal(m["_rev"], &rev)

	var names []string
	if raw, ok := m["_attachments"]; ok {
		var atts map[string]json.RawMessage
		if err := json.Unmarshal(raw, &atts); err != nil {
			return nil, nil, "", "", err
		}
		for name := range atts {
			names = append(names, name)
		}
		delete(m, "_attachments")
	}
	// Map iteration order is random; a dump has to be reproducible.
	sort.Strings(names)
	stripped, err := json.Marshal(m)
	if err != nil {
		return nil, nil, "", "", err
	}
	return stripped, names, id, rev, nil
}

// writeAttachment spills an attachment to a temp file to learn its exact size,
// then writes the att record and copies the bytes into the dump. It returns the
// number of payload bytes written, for the progress counter.
//
// CouchDB's reported attachment length is the gzip-compressed size when it
// stored the attachment compressed, and a streamed GET has no Content-Length,
// so the byte count has to be measured locally. The spill keeps memory bounded:
// the attachment goes disk to disk in fixed-size chunks, never into a buffer.
func writeAttachment(ctx context.Context, s *session.Session, w *backup.Writer, db, id, rev, name string) (int64, error) {
	att, err := s.Client.GetAttachment(ctx, db, id, name, rev)
	if err != nil {
		return 0, err
	}
	defer att.Content.Close()

	// CreateTemp makes the file 0600, which matters: an attachment can hold
	// anything the database holds.
	spill, err := os.CreateTemp("", "cdb-att")
	if err != nil {
		return 0, err
	}
	defer func() {
		spill.Close()
		os.Remove(spill.Name())
	}()

	n, err := io.Copy(spill, att.Content)
	if err != nil {
		return 0, err
	}
	if _, err := spill.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if err := w.WriteAttachment(backup.Att{
		ID:          id,
		Rev:         rev,
		Name:        name,
		ContentType: att.ContentType,
		Length:      n,
	}, spill); err != nil {
		return 0, err
	}
	return n, nil
}
