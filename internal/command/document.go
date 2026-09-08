package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Cat returns the cat command.
func Cat() Command {
	return Command{
		Name:        "cat",
		Summary:     "Print a document or an attachment",
		Usage:       "<path>",
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("rev", "", "revision to read")
			fs.Bool("revs", false, "include the revision history")
			fs.Bool("conflicts", false, "include conflicting revisions")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			switch t.Kind {
			case path.KindDocument, path.KindDesignDoc:
				body, _, err := s.Client.GetDocument(ctx, t.Database, t.DocID, couch.GetOptions{
					Rev:       inv.String("rev"),
					Revs:      inv.Bool("revs"),
					Conflicts: inv.Bool("conflicts"),
				})
				if err != nil {
					return nil, err
				}
				return Document{JSON: body}, nil
			case path.KindAttachment:
				return catAttachment(ctx, s, t, inv.String("rev"))
			default:
				return nil, Usagef("cat", "%s is a %s; use \"ls\" to list it.", t.Path, t.Kind)
			}
		},
	}
}

// catAttachment is replaced with a streamed implementation in Task 13. Until
// then it prints the document the attachment hangs off, which at least tells
// the operator the attachment's content type and length.
func catAttachment(ctx context.Context, s *session.Session, t path.Target, rev string) (Result, error) {
	body, _, err := s.Client.GetDocument(ctx, t.Database, t.DocID, couch.GetOptions{Rev: rev})
	if err != nil {
		return nil, err
	}
	return Document{JSON: body}, nil
}

// Put returns the put command.
func Put() Command {
	return Command{
		Name:        "put",
		Summary:     "Create or update a document from a file or standard input",
		Usage:       "<path> [file]",
		MinArgs:     1,
		MaxArgs:     2,
		NeedsClient: true,
		Complete:    completePath,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDocument && t.Kind != path.KindDesignDoc {
				return nil, Usagef("put", "%s is a %s, not a document", t.Path, t.Kind)
			}
			raw, err := readDocSource(s, inv.Arg(1))
			if err != nil {
				return nil, err
			}
			doc, rev, err := normaliseDoc(raw)
			if err != nil {
				return nil, err
			}
			if rev == "" {
				current, err := s.Client.GetRev(ctx, t.Database, t.DocID)
				if err != nil {
					// A missing document is the create case, not a failure.
					if ce, ok := couch.AsError(err); !ok || ce.Status != 404 {
						return nil, err
					}
				}
				if current != "" {
					if err := Confirm(s, fmt.Sprintf("%s already exists at revision %s. Overwrite it?", t.Path, current)); err != nil {
						return nil, err
					}
					rev = current
				}
			}
			newRev, err := s.Client.PutDocument(ctx, t.Database, t.DocID, doc, rev)
			if err != nil {
				return nil, err
			}
			return Message{Text: fmt.Sprintf("Wrote %s at revision %s.", t.Path, newRev)}, nil
		},
	}
}

// readDocSource reads the document body from a file, or from stdin when the
// file argument is absent or "-".
func readDocSource(s *session.Session, file string) ([]byte, error) {
	if file == "" || file == "-" {
		return io.ReadAll(s.Reader())
	}
	return os.ReadFile(file)
}

// normaliseDoc validates JSON and pulls out _rev. The body is passed through
// unchanged: decoding and re-encoding it would reorder the keys and round every
// large integer through float64.
func normaliseDoc(raw []byte) (json.RawMessage, string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", fmt.Errorf("the input is not a JSON object: %w", err)
	}
	var rev string
	if r, ok := m["_rev"]; ok {
		_ = json.Unmarshal(r, &rev)
	}
	return json.RawMessage(raw), rev, nil
}

// Rm returns the rm command.
func Rm() Command {
	return Command{
		Name:        "rm",
		Summary:     "Delete a document or an attachment",
		Usage:       "<path>",
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Destructive: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("rev", "", "revision to delete")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			switch t.Kind {
			case path.KindDocument, path.KindDesignDoc:
			case path.KindAttachment:
				// Stand-in: Client.DeleteAttachment does not exist yet. Task 13
				// removes this case and deletes the attachment after the same
				// rev lookup and confirmation the document path uses. Refusing
				// here, before the prompt, keeps cdb from asking a question it
				// cannot act on.
				return nil, Usagef("rm", "deleting attachments is added in a later step; delete the document instead")
			case path.KindDatabase:
				return nil, Usagef("rm", "%s is a database; use \"rmdir\" to delete it.", t.Path)
			default:
				return nil, Usagef("rm", "%s is a %s; only documents and attachments can be deleted.", t.Path, t.Kind)
			}
			rev := inv.String("rev")
			if rev == "" {
				rev, err = s.Client.GetRev(ctx, t.Database, t.DocID)
				if err != nil {
					return nil, err
				}
			}
			if err := Confirm(s, fmt.Sprintf("Delete %s at revision %s?", t.Path, rev)); err != nil {
				return nil, err
			}
			newRev, err := s.Client.DeleteDocument(ctx, t.Database, t.DocID, rev)
			if err != nil {
				return nil, err
			}
			return Message{Text: fmt.Sprintf("Deleted %s. The tombstone revision is %s.", t.Path, newRev)}, nil
		},
	}
}

// maxEditAttempts caps edit's write-conflict retry loop. Three tries is enough
// to get past a document somebody else happened to save at the same moment,
// and small enough that --yes cannot turn a busy document into a spin.
const maxEditAttempts = 3

// withRev replaces the top-level "_rev" of a JSON object with rev and leaves
// every other byte alone: key order, number literals, and any nested object
// that has a "_rev" of its own all survive. A document with no top-level
// "_rev" comes back unchanged, and the revision then travels in the query
// string instead of the body.
//
// Rebuilding the object through a map would be shorter and wrong: Go marshals
// map keys in sorted order, so the operator's document would come back
// reordered on every conflict retry.
func withRev(doc json.RawMessage, rev string) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("the document is not a JSON object")
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		// InputOffset sits between the key and its colon here, and just past
		// the value once it has been decoded, which brackets the span to swap.
		start := dec.InputOffset()
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if key != "_rev" {
			continue
		}
		end := dec.InputOffset()
		quoted, err := json.Marshal(rev)
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, len(doc)+len(quoted))
		out = append(out, doc[:start]...)
		out = append(out, ':')
		out = append(out, quoted...)
		out = append(out, doc[end:]...)
		return out, nil
	}
	return doc, nil
}

// Edit returns the edit command.
func Edit() Command {
	return Command{
		Name:        "edit",
		Summary:     "Open a document in $EDITOR and save the result",
		Usage:       "<path>",
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDocument && t.Kind != path.KindDesignDoc {
				return nil, Usagef("edit", "%s is a %s, not a document", t.Path, t.Kind)
			}
			editor := editorCommand()
			if editor == "" {
				return nil, Usagef("edit", "no editor is configured; set $EDITOR or $VISUAL")
			}
			body, rev, err := s.Client.GetDocument(ctx, t.Database, t.DocID, couch.GetOptions{})
			if err != nil {
				return nil, err
			}
			edited, changed, err := editBuffer(editor, body)
			if err != nil {
				return nil, err
			}
			if !changed {
				return Message{Text: "No changes; nothing was written."}, nil
			}
			doc, bodyRev, err := normaliseDoc(edited)
			if err != nil {
				return nil, err
			}
			if bodyRev != "" {
				rev = bodyRev
			}
			// On a conflict the edited buffer is what the operator wants kept,
			// so the retry moves it onto the newest revision rather than
			// reopening the editor on the server's copy, which would throw the
			// edits away. The cap keeps --yes from spinning against a document
			// somebody else is writing to in a loop.
			for attempt := 1; ; attempt++ {
				newRev, err := s.Client.PutDocument(ctx, t.Database, t.DocID, doc, rev)
				if err == nil {
					return Message{Text: fmt.Sprintf("Wrote %s at revision %s.", t.Path, newRev)}, nil
				}
				ce, ok := couch.AsError(err)
				if !ok || ce.Status != 409 || attempt >= maxEditAttempts {
					return nil, err
				}
				fmt.Fprintf(s.Stdout, "%s was changed by someone else while you were editing.\n", t.Path)
				if cerr := Confirm(s, "Reapply your edits on top of the latest revision?"); cerr != nil {
					return nil, err
				}
				latestRev, gerr := s.Client.GetRev(ctx, t.Database, t.DocID)
				if gerr != nil {
					return nil, gerr
				}
				if doc, err = withRev(doc, latestRev); err != nil {
					return nil, err
				}
				rev = latestRev
			}
		},
	}
}

// editorCommand is $VISUAL, falling back to $EDITOR. The value is trimmed so
// that a variable holding only spaces reads as unset rather than reaching
// exec.Command with no words in it.
func editorCommand() string {
	if v := strings.TrimSpace(os.Getenv("VISUAL")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("EDITOR"))
}

// editBuffer writes body to a temp file, runs the editor on it, and reports
// whether the content changed.
//
// The editor gets the process's own stdio, not the session's writers: a
// full-screen editor needs the real terminal, and handing os/exec a writer that
// is not a file would put a pipe between the editor and the tty.
func editBuffer(editor string, body []byte) ([]byte, bool, error) {
	pretty, err := indentJSON(body)
	if err != nil {
		pretty = body
	}
	dir, err := os.MkdirTemp("", "cdb-edit")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "document.json")
	if err := os.WriteFile(name, pretty, 0o600); err != nil {
		return nil, false, err
	}
	fields := strings.Fields(editor)
	cmd := exec.Command(fields[0], append(fields[1:], name)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, false, fmt.Errorf("editor exited with an error: %w", err)
	}
	edited, err := os.ReadFile(name)
	if err != nil {
		return nil, false, err
	}
	return edited, !bytes.Equal(edited, pretty), nil
}

// indentJSON pretty-prints a document for the editor.
//
// It must use json.Indent, never a map[string]any round trip. Unmarshalling
// into a map sorts every key (moving _id and _rev around, which makes every
// edit look like a rewrite) and decodes every number as float64, so a 19-digit
// identifier comes back as 1.2345678901234568e+18 and would be silently
// written back to CouchDB. json.Indent preserves key order and numeric
// literals byte for byte.
func indentJSON(b []byte) ([]byte, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
