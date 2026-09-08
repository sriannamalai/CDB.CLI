package command

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Attach returns the attach command.
func Attach() Command {
	return Command{
		Name:    "attach",
		Summary: "Upload a file as an attachment",
		Example: `$ cdb attach /movies/tt0211915 ./poster.txt
Attached poster.txt (17 B) to /movies/tt0211915. The document is now at revision 5-ac866d9c221230c601e098ecae4e1c4d.

$ cdb attach /movies/tt0211915 ./cover.bin --name cover.png --content-type image/png`,
		Usage:       "<doc-path> <file>",
		MinArgs:     2,
		MaxArgs:     2,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("name", "", "attachment name, defaulting to the file's base name")
			fs.String("content-type", "", "content type, guessed from the file extension by default")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDocument && t.Kind != path.KindDesignDoc {
				return nil, Usagef("attach", "%s is %s %s; attach needs a document path", t.Path, t.Kind.Article(), t.Kind)
			}
			file := inv.Arg(1)
			// The file is opened, never read: it goes straight into the request
			// body so that a multi-gigabyte attachment never lands in memory.
			f, err := os.Open(file)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			fi, err := f.Stat()
			if err != nil {
				return nil, err
			}
			name := inv.String("name")
			if name == "" {
				name = filepath.Base(file)
			}
			ctype := inv.String("content-type")
			if ctype == "" {
				ctype = mime.TypeByExtension(filepath.Ext(file))
			}
			rev, err := s.Client.GetRev(ctx, t.Database, t.DocID)
			if err != nil {
				return nil, err
			}
			newRev, err := s.Client.PutAttachment(ctx, t.Database, t.DocID, name, ctype, fi.Size(), f, rev)
			if err != nil {
				return nil, err
			}
			return Message{Text: fmt.Sprintf("Attached %s (%s) to %s. The document is now at revision %s.", name, humanBytes(fi.Size()), t.Path, newRev)}, nil
		},
	}
}

// Fetch returns the fetch command.
func Fetch() Command {
	return Command{
		Name:    "fetch",
		Summary: "Download an attachment",
		Example: `$ cdb fetch /movies/tt0211915/poster.txt
Wrote poster.txt (17 B) from /movies/tt0211915/poster.txt.

$ cdb fetch /movies/tt0211915/poster.txt /tmp/poster.txt --force`,
		Usage:       "<attachment-path> [out-file]",
		MinArgs:     1,
		MaxArgs:     2,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("rev", "", "revision to read the attachment from")
			fs.Bool("force", false, "overwrite an existing output file")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindAttachment {
				return nil, Usagef("fetch", "%s is %s %s; fetch needs an attachment path such as /mydb/doc1/photo.jpg", t.Path, t.Kind.Article(), t.Kind)
			}
			out := inv.Arg(1)
			if out != "-" {
				if out == "" {
					out = t.Attachment
				}
				// Refuse before opening the connection: there is no point
				// downloading a gigabyte only to find there is nowhere to put it.
				if !inv.Bool("force") {
					switch _, err := os.Stat(out); {
					case err == nil:
						return nil, Usagef("fetch", "%s already exists; pass --force to overwrite it", out)
					case !os.IsNotExist(err):
						return nil, err
					}
				}
			}
			att, err := s.Client.GetAttachment(ctx, t.Database, t.DocID, t.Attachment, inv.String("rev"))
			if err != nil {
				return nil, err
			}
			if out == "-" {
				return Raw{Reader: &closeAfterRead{rc: att.Content}, ContentType: att.ContentType, Name: t.Attachment}, nil
			}
			defer att.Content.Close()
			n, err := downloadTo(out, att.Content)
			if err != nil {
				return nil, err
			}
			return Message{Text: fmt.Sprintf("Wrote %s (%s) from %s.", out, humanBytes(n), t.Path)}, nil
		},
	}
}

// downloadTo streams src into a temporary file beside dst and renames it over
// dst only once every byte has arrived and been flushed to disk. It returns the
// number of bytes written, which is the only trustworthy byte count: CouchDB's
// reported length can be the gzip-compressed one.
//
// Writing straight into dst would be shorter and would lose data: a dropped
// connection or a Ctrl-C part way through a large attachment would leave a
// truncated file, and with --force the operator's previous good copy would
// already have been destroyed before the first byte arrived. The temporary file
// goes in the same directory so the rename stays on one filesystem, and it is
// removed again on any failure, leaving dst exactly as it was.
//
// io.Copy, not io.ReadAll: the attachment reaches disk in fixed-size chunks
// whatever its size.
func downloadTo(dst string, src io.Reader) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.part")
	if err != nil {
		return 0, err
	}
	name := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	n, err := io.Copy(tmp, src)
	if err != nil {
		return 0, err
	}
	// Sync before the rename: without it a crash could leave the renamed file
	// present but empty.
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(name, dst); err != nil {
		return 0, err
	}
	renamed = true
	return n, nil
}

// closeAfterRead closes the underlying stream when it reaches EOF. render.Render
// copies a Raw result but does not close it, so this keeps the response body
// from leaking when an attachment is streamed to stdout.
type closeAfterRead struct{ rc io.ReadCloser }

func (c *closeAfterRead) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if err != nil {
		_ = c.rc.Close()
	}
	return n, err
}
