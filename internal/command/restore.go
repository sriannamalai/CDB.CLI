package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/backup"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// inlineAttachmentLimit is the largest attachment restored inline as base64
// inside the _bulk_docs body. Inlining preserves the original revision;
// anything larger is uploaded separately, which bumps the revision.
const inlineAttachmentLimit = 4 << 20 // 4 MiB

// restoreBatchSize is how many documents are written per _bulk_docs call.
const restoreBatchSize = 200

// restoreBatchBytes caps the payload one batch may hold before it is flushed
// early. Without it a batch of documents that each carry an attachment would
// hold restoreBatchSize * inlineAttachmentLimit bytes of base64 in memory, and
// keep every oversized attachment's spill file on disk until the batch closed.
const restoreBatchBytes = 16 << 20 // 16 MiB

// pendingDoc is a document waiting to be written, with any attachments that
// arrived with it.
type pendingDoc struct {
	id     string
	rev    string
	fields map[string]json.RawMessage
	inline map[string]json.RawMessage
	large  []largeAttachment
	// inlineBytes is the base64 this document contributes to the request body;
	// bytes is that plus the document itself and any spilled payload. They are
	// kept per document so the batch totals can be rebuilt when the documents
	// ahead of this one are written out on their own.
	inlineBytes int64
	bytes       int64
	// deleted marks a tombstone. It needs no special write path — _bulk_docs
	// with new_edits=false stores it with its supplied _rev and _revisions,
	// which is what makes the deletion replicate onward — but an attachment
	// record following one is a corrupt dump.
	deleted bool
}

// largeAttachment is an attachment spilled to a temp file because it exceeds
// inlineAttachmentLimit.
type largeAttachment struct {
	name        string
	contentType string
	size        int64
	file        string
}

// Restore returns the restore command.
func Restore() Command {
	return Command{
		Name:    "restore",
		Summary: "Load a dump file into a database",
		Example: `$ cdb restore movies.cdb.gz /movies-copy --create
5 documents, 1 attachments, 1.1 KB...
Restored 5 document(s), 1 attachment(s) and 1.1 KB into "movies-copy".

$ cdb restore movies.cdb.gz /movies --merge`,
		Usage:       "<file> <db-path>",
		MinArgs:     2,
		MaxArgs:     2,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("create", false, "create the target database if it does not exist")
			fs.Bool("merge", false, "allow restoring into a database that already has documents")
			fs.Bool("partial", false, "restore a dump that has no footer, and so is incomplete")
			fs.Int("batch", restoreBatchSize, "documents per bulk write")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			file := inv.Arg(0)
			t, err := s.Resolve(inv.Arg(1))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDatabase {
				return nil, Usagef("restore", "%s is %s %s; restore takes a database path", t.Path, t.Kind.Article(), t.Kind)
			}
			batch := inv.Int("batch")
			if batch <= 0 {
				batch = restoreBatchSize
			}

			f, err := os.Open(file)
			if err != nil {
				return nil, err
			}
			defer f.Close()

			// Walk the dump once before touching the server, so that a file
			// that is not a dump at all, or one an interrupted backup left
			// without a footer, is refused before anything is written. Scan
			// reports neither a torn record nor a short attachment payload —
			// it stops at the last intact gzip member — so the read below
			// still has to surface those itself.
			scan, err := backup.Scan(f)
			if err != nil {
				var ve *backup.VersionError
				if errors.As(err, &ve) {
					return nil, Usagef("restore", "%s was written by a newer cdb (dump format %s); upgrade cdb to read it.", file, ve.Version)
				}
				if errors.Is(err, backup.ErrNotADump) {
					// Naming the wrong file is a usage mistake, so exit 2, and
					// under the command the operator actually ran.
					return nil, Usagef("restore", "%s is not a cdb dump: it was not written by \"cdb backup\"", file)
				}
				return nil, err
			}
			if !scan.Complete && !inv.Bool("partial") {
				return nil, Usagef("restore", "%s has no footer: an interrupted backup left it incomplete, so it holds only part of the database. Pass --partial to load what it does hold", file)
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			// An incomplete dump is read only as far as its last intact gzip
			// member. A backup interrupted with Ctrl-C leaves a half-written
			// member behind, and that is the ordinary shape of the dump
			// --partial exists for; reading into it would fail on the torn
			// bytes instead of loading the records that survived. Records
			// inside the intact prefix are still checked in full, so a short
			// attachment payload there is reported rather than restored.
			var src io.Reader = f
			if !scan.Complete {
				src = io.LimitReader(f, scan.Offset)
			}
			r, err := backup.NewReader(src)
			if err != nil {
				return nil, err
			}
			defer r.Close()

			var (
				header              *backup.Header
				footer              *backup.Footer
				prepared            bool
				pending             []*pendingDoc
				pendingBytes        int64
				inlineBytes         int64
				current             *pendingDoc
				docs, atts, written int64
				deleted             int64
				bumped              []string
				tempFiles           []string
				progressed          bool
			)
			defer func() {
				for _, p := range tempFiles {
					os.Remove(p)
				}
			}()
			// The progress line is rewritten in place with a carriage return,
			// so it has to be closed on every exit — including a Ctrl-C, whose
			// error would otherwise be printed onto the end of it.
			defer func() {
				if progressed {
					fmt.Fprintln(s.Stderr)
				}
			}()

			writeBatch := func(batch []*pendingDoc) error {
				if len(batch) == 0 {
					return nil
				}
				bodies := make([]json.RawMessage, 0, len(batch))
				for _, p := range batch {
					body, err := p.marshal()
					if err != nil {
						return err
					}
					bodies = append(bodies, body)
				}
				failures, err := s.Client.BulkDocs(ctx, t.Database, bodies, false)
				if err != nil {
					return err
				}
				if len(failures) > 0 {
					msgs := make([]string, 0, len(failures))
					for _, fl := range failures {
						msgs = append(msgs, fmt.Sprintf("%s: %s (%s)", fl.ID, fl.Error, fl.Reason))
					}
					return fmt.Errorf("the server rejected %d document(s): %s", len(failures), strings.Join(msgs, "; "))
				}
				// An attachment too large to inline goes up on its own, which
				// costs the document the revision the bulk write just gave it.
				for _, p := range batch {
					for _, la := range p.large {
						newRev, err := uploadSpill(ctx, s, t.Database, p, la)
						if err != nil {
							return err
						}
						bumped = append(bumped, fmt.Sprintf("%s (%s -> %s)", p.id, p.rev, newRev))
						p.rev = newRev
					}
				}
				// Spec section 9: progress carries document and byte counts.
				fmt.Fprintf(s.Stderr, "\r%d documents, %d attachments, %s…", docs, atts, humanBytes(written))
				progressed = true
				return nil
			}

			flush := func() error {
				current = nil
				if err := writeBatch(pending); err != nil {
					return err
				}
				pending = pending[:0]
				pendingBytes = 0
				inlineBytes = 0
				return nil
			}

			// flushBefore writes out every document ahead of the one being
			// read, leaving that one alone in a fresh batch. It is how the byte
			// bound is kept at a document boundary: a document is never stripped
			// of an attachment it could carry merely because the documents
			// before it had filled the request.
			flushBefore := func() error {
				if len(pending) < 2 {
					return nil
				}
				last := pending[len(pending)-1]
				if err := writeBatch(pending[:len(pending)-1]); err != nil {
					return err
				}
				pending = append(pending[:0], last)
				inlineBytes = last.inlineBytes
				pendingBytes = last.bytes
				return nil
			}

			first := true
			for {
				rec, err := r.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return nil, fmt.Errorf("restore: reading %s: %w", file, err)
				}
				// A dump opens with a header. Scan has already refused a file
				// whose first member does not, but the guarantee is cheap to
				// keep next to the code that relies on it.
				if first {
					if rec.Kind != backup.KindHeader {
						return nil, fmt.Errorf("restore: %s is not a cdb dump: it begins with a %q record, not a header", file, rec.Kind)
					}
					first = false
				}

				switch rec.Kind {
				case backup.KindHeader:
					header = rec.Header

				case backup.KindFooter:
					footer = rec.Footer

				case backup.KindDoc:
					if !prepared {
						if err := prepareTarget(ctx, s, t, header, inv.Bool("create"), inv.Bool("merge")); err != nil {
							return nil, err
						}
						prepared = true
					}
					// The batch is closed here, at a document boundary, and
					// never after appending: an attachment record follows its
					// document, and flushing between the two would leave the
					// attachment to a separate upload that bumps the revision
					// the batch had just restored.
					if len(pending) >= batch || pendingBytes >= restoreBatchBytes {
						if err := flush(); err != nil {
							return nil, err
						}
					}
					p, err := newPendingDoc(rec.Doc)
					if err != nil {
						return nil, err
					}
					p.bytes = int64(len(rec.Doc))
					pending = append(pending, p)
					current = p
					docs++
					if p.deleted {
						deleted++
					}
					written += int64(len(rec.Doc))
					pendingBytes += p.bytes

				case backup.KindAtt:
					a := rec.Att
					if current == nil || current.id != a.ID || current.rev != a.Rev {
						return nil, fmt.Errorf("restore: %s: attachment %q belongs to %s@%s, which is not the document that precedes it in the dump", file, a.Name, a.ID, a.Rev)
					}
					if current.deleted {
						return nil, fmt.Errorf("restore: %s: attachment %q follows the deletion of %q, which can hold no attachments.", file, a.Name, a.ID)
					}
					// Length comes off the wire, and it sizes an allocation
					// below; a negative one would panic rather than report the
					// corrupt record it came from.
					if a.Length < 0 {
						return nil, fmt.Errorf("restore: %s: attachment %q of %s@%s declares a length of %d", file, a.Name, a.ID, a.Rev, a.Length)
					}
					// What the request body actually holds is the base64 form,
					// a third larger than the payload, so that is what counts
					// towards the bound. Reaching it is normally a reason to
					// close the batch, not to strip this document: the
					// documents ahead of it go out on their own and it starts a
					// fresh batch. Only a document whose own attachments cannot
					// fit in an empty batch falls back to the streamed path,
					// which costs it the revision the dump recorded.
					encoded := int64(base64.StdEncoding.EncodedLen(int(a.Length)))
					if a.Length <= inlineAttachmentLimit && inlineBytes+encoded > restoreBatchBytes {
						if err := flushBefore(); err != nil {
							return nil, err
						}
					}
					if a.Length <= inlineAttachmentLimit && inlineBytes+encoded <= restoreBatchBytes {
						entry, err := inlineEntry(rec.Content, a)
						if err != nil {
							return nil, fmt.Errorf("restore: reading %s: %w", file, err)
						}
						if current.inline == nil {
							current.inline = map[string]json.RawMessage{}
						}
						current.inline[a.Name] = entry
						current.inlineBytes += encoded
						current.bytes += encoded
						inlineBytes += encoded
						pendingBytes += encoded
					} else {
						name, err := spillAttachment(rec.Content, a.Length)
						if err != nil {
							return nil, fmt.Errorf("restore: reading %s: %w", file, err)
						}
						tempFiles = append(tempFiles, name)
						current.large = append(current.large, largeAttachment{
							name:        a.Name,
							contentType: a.ContentType,
							size:        a.Length,
							file:        name,
						})
						// A spilled attachment is on disk rather than in the
						// batch body, but its temp file lives until the batch
						// closes, so it still counts towards flushing early.
						current.bytes += a.Length
						pendingBytes += a.Length
					}
					atts++
					written += a.Length
				}
			}
			if !prepared {
				// An empty dump still has to create the target when --create
				// was passed, and still has to refuse a non-empty one.
				if err := prepareTarget(ctx, s, t, header, inv.Bool("create"), inv.Bool("merge")); err != nil {
					return nil, err
				}
			}
			if err := flush(); err != nil {
				return nil, err
			}
			// The footer is the backup's own count of what it wrote. A complete
			// dump that yields fewer records than it claims has lost some
			// between the two, and reporting success would hide it. A dump read
			// under --partial is deliberately truncated, so there is nothing to
			// compare against.
			if footer != nil && !inv.Bool("partial") {
				if footer.Docs != docs {
					return nil, fmt.Errorf("restore: %s: the footer records %d document(s) but %d were restored; the dump is damaged", file, footer.Docs, docs)
				}
				if footer.Attachments != atts {
					return nil, fmt.Errorf("restore: %s: the footer records %d attachment(s) but %d were restored; the dump is damaged", file, footer.Attachments, atts)
				}
				if footer.Deleted != deleted {
					return nil, fmt.Errorf("restore: %s: the footer records %d deletion(s) but %d were restored; the dump is damaged", file, footer.Deleted, deleted)
				}
			}

			text := fmt.Sprintf("Restored %d document(s)", docs)
			if deleted > 0 {
				text += fmt.Sprintf(" (%d of them deletions)", deleted)
			}
			text += fmt.Sprintf(", %d attachment(s) and %s into %q.", atts, humanBytes(written), t.Database)
			if len(bumped) > 0 {
				text += fmt.Sprintf("\n%d document(s) changed revision because their attachments were too large to inline: %s", len(bumped), strings.Join(bumped, ", "))
			}
			return Message{Text: text}, nil
		},
	}
}

// prepareTarget creates or validates the destination database.
func prepareTarget(ctx context.Context, s *session.Session, t path.Target, header *backup.Header, create, merge bool) error {
	exists, err := s.Client.DatabaseExists(ctx, t.Database)
	if err != nil {
		return err
	}
	if !exists {
		if !create {
			return Usagef("restore", "database %q does not exist; pass --create to create it", t.Database)
		}
		partitioned := header != nil && header.Partitioned
		return s.Client.CreateDatabase(ctx, t.Database, partitioned, 0)
	}
	info, err := s.Client.DatabaseInfo(ctx, t.Database)
	if err != nil {
		return err
	}
	if info.DocCount > 0 && !merge {
		return Usagef("restore", "database %q already holds %d document(s); pass --merge to restore into it anyway", t.Database, info.DocCount)
	}
	return nil
}

// newPendingDoc decodes a dump document for later writing.
func newPendingDoc(body json.RawMessage) (*pendingDoc, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	p := &pendingDoc{fields: m}
	_ = json.Unmarshal(m["_id"], &p.id)
	_ = json.Unmarshal(m["_rev"], &p.rev)
	p.deleted = backup.IsTombstone(body)
	return p, nil
}

// marshal renders the document, inlining any small attachments so that
// _bulk_docs with new_edits=false keeps the original revision.
func (p *pendingDoc) marshal() (json.RawMessage, error) {
	// Verified on CouchDB 3.5.2: new_edits=false answers 412 "Invalid
	// attachment stub" for a document that still carries one. Backup strips
	// them, and the only _attachments this writes back is the inline data
	// rebuilt from the dump's own attachment records.
	delete(p.fields, "_attachments")
	if len(p.inline) > 0 {
		atts, err := json.Marshal(p.inline)
		if err != nil {
			return nil, err
		}
		p.fields["_attachments"] = atts
	}
	return json.Marshal(p.fields)
}

// inlineEntry reads an attachment payload and renders it as the base64
// _attachments entry that _bulk_docs stores without changing the revision.
// It reads exactly the declared length, so a dump whose tail was lost is
// reported rather than restored as a short attachment.
func inlineEntry(content io.Reader, a *backup.Att) (json.RawMessage, error) {
	data := make([]byte, a.Length)
	if _, err := io.ReadFull(content, data); err != nil {
		return nil, fmt.Errorf("attachment %q of %s@%s: %w", a.Name, a.ID, a.Rev, err)
	}
	return json.Marshal(map[string]string{
		"content_type": a.ContentType,
		"data":         base64.StdEncoding.EncodeToString(data),
	})
}

// spillAttachment copies exactly length bytes into a temp file and returns its
// path. The caller removes the file.
func spillAttachment(r io.Reader, length int64) (string, error) {
	// CreateTemp makes the file 0600, which matters: an attachment can hold
	// anything the database holds.
	f, err := os.CreateTemp("", "cdb-restore")
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, length))
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if n != length {
		os.Remove(f.Name())
		return "", fmt.Errorf("attachment payload was %d bytes, expected %d", n, length)
	}
	return f.Name(), nil
}

// uploadSpill streams a spilled attachment into the document and returns the
// revision the upload produced. The temp file is dropped as soon as it is on
// the server, so a long restore does not accumulate copies of every oversized
// attachment it has already written.
func uploadSpill(ctx context.Context, s *session.Session, db string, p *pendingDoc, la largeAttachment) (string, error) {
	f, err := os.Open(la.file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	newRev, err := s.Client.PutAttachment(ctx, db, p.id, la.name, la.contentType, la.size, f, p.rev)
	if err != nil {
		return "", err
	}
	os.Remove(la.file)
	return newRev, nil
}
