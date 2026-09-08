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
		Name:        "attach",
		Summary:     "Upload a file as an attachment",
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
				return nil, Usagef("attach", "%s is a %s; attach needs a document path", t.Path, t.Kind)
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
		Name:        "fetch",
		Summary:     "Download an attachment",
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
				return nil, Usagef("fetch", "%s is a %s; fetch needs an attachment path such as /mydb/doc1/photo.jpg", t.Path, t.Kind)
			}
			att, err := s.Client.GetAttachment(ctx, t.Database, t.DocID, t.Attachment, inv.String("rev"))
			if err != nil {
				return nil, err
			}
			out := inv.Arg(1)
			if out == "-" {
				return Raw{Reader: &closeAfterRead{rc: att.Content}, ContentType: att.ContentType, Name: t.Attachment}, nil
			}
			defer att.Content.Close()
			if out == "" {
				out = t.Attachment
			}
			flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			if !inv.Bool("force") {
				flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
			}
			f, err := os.OpenFile(out, flags, 0o644)
			if err != nil {
				if os.IsExist(err) {
					return nil, Usagef("fetch", "%s already exists; pass --force to overwrite it", out)
				}
				return nil, err
			}
			defer f.Close()
			// io.Copy, not io.ReadAll: the attachment is streamed to disk in
			// fixed-size chunks whatever its size. The count it returns is the
			// only trustworthy byte count, because CouchDB's reported length
			// can be the gzip-compressed one.
			n, err := io.Copy(f, att.Content)
			if err != nil {
				return nil, err
			}
			return Message{Text: fmt.Sprintf("Wrote %s (%s) from %s.", out, humanBytes(n), t.Path)}, nil
		},
	}
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
