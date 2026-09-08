package backup

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
)

// Resume describes where an interrupted dump can be continued.
type Resume struct {
	// Seq is the sequence of the last complete checkpoint, or "" if none.
	Seq string
	// Offset is the byte offset just past the gzip member that ended with that
	// checkpoint. Truncate the file here before appending.
	Offset int64
	// Docs and Attachments are the counts recorded up to that checkpoint, and
	// Bytes is the document and attachment payload size they add up to. The
	// backup command resumes its progress counters from these.
	Docs, Attachments, Bytes int64
	// Complete is true when the dump already contains a footer. A complete dump
	// must not be resumed: it has nothing left to append.
	Complete bool
}

// countingReader tracks the logical byte offset consumed from the file. It
// implements io.ByteReader so compress/gzip does not wrap it in its own
// bufio.Reader and read past a member boundary.
type countingReader struct {
	br *bufio.Reader
	n  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.br.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReader) ReadByte() (byte, error) {
	b, err := c.br.ReadByte()
	if err == nil {
		c.n++
	}
	return b, err
}

// ErrNotADump means the file exists and is readable but was not written by
// "cdb backup". Callers match on it to report the mistake as their own usage
// error rather than passing this package's sentence through.
var ErrNotADump = errors.New("is not a cdb dump")

// Scan walks an existing dump file member by member and reports the last point
// at which it can be safely resumed. A partially written trailing member is
// ignored.
//
// The first record of the first member must be a header, so that Scan cannot
// hand back Offset 0 for a file that is not a cdb dump — a caller acting on that
// would truncate an unrelated file to nothing. An empty or truncated-to-nothing
// file is not an error; it reports the zero Resume, meaning start from scratch.
//
// Scan leaves f at an unspecified position. Truncate the dump by path and reopen
// it rather than reusing f for further reads or writes.
func Scan(f *os.File) (Resume, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Resume{}, err
	}
	var (
		res   Resume
		cr    = &countingReader{br: bufio.NewReaderSize(f, 64*1024)}
		docs  int64
		atts  int64
		bytes int64
	)
	gz, err := gzip.NewReader(cr)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return res, nil
		}
		return res, err
	}
	defer gz.Close()
	// notADump reports a file whose first member does not open with a header
	// record. Returning Resume{Offset: 0} for one would invite the caller to
	// truncate an unrelated gzip file to nothing.
	notADump := func() (Resume, error) {
		// Wrapped rather than formatted in, so a caller can recognise this
		// without matching on the sentence: naming the wrong file is a usage
		// mistake, and the command that reports it is the one the operator
		// ran, not this package.
		return Resume{}, fmt.Errorf("%s %w", f.Name(), ErrNotADump)
	}
	first := true
	for {
		gz.Multistream(false)
		r := &Reader{gz: gz, br: newBufReader(gz)}
		memberOK := true
		// Everything a member reports is provisional until the member reads to
		// its end. A truncated trailing member can decode a checkpoint record
		// and then fail on the gzip trailer; committing that sequence would
		// leave Seq newer than Offset, and --resume would truncate to the older
		// offset while restarting from the newer sequence, silently dropping
		// every document in between.
		memberSeq := res.Seq
		memberComplete := res.Complete
		for {
			rec, rerr := r.Next()
			if errors.Is(rerr, io.EOF) {
				if first {
					return notADump()
				}
				break
			}
			if rerr != nil {
				if first {
					return notADump()
				}
				memberOK = false
				break
			}
			if first {
				if rec.Kind != KindHeader {
					return notADump()
				}
				first = false
			}
			switch rec.Kind {
			case KindDoc:
				docs++
				bytes += int64(len(rec.Doc))
			case KindAtt:
				atts++
				bytes += rec.Att.Length
				if _, cerr := io.Copy(io.Discard, rec.Content); cerr != nil {
					memberOK = false
				}
			case KindCheckpoint:
				memberSeq = rec.Checkpoint
			case KindFooter:
				memberComplete = true
			}
			if !memberOK {
				break
			}
		}
		if !memberOK {
			break
		}
		// Commit Offset, the counters, Seq and Complete together.
		res.Offset = cr.n
		res.Docs = docs
		res.Attachments = atts
		res.Bytes = bytes
		res.Seq = memberSeq
		res.Complete = memberComplete
		if rerr := gz.Reset(cr); rerr != nil {
			break
		}
	}
	return res, nil
}

// Truncate cuts a dump file back to the given byte offset. It refuses an offset
// past the end of the file: os.Truncate would zero-extend there, padding the
// dump with NUL bytes and corrupting it rather than reporting the stale offset.
func Truncate(path string, offset int64) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if offset < 0 || offset > fi.Size() {
		return fmt.Errorf("backup: truncate %s to %d: file is %d bytes", path, offset, fi.Size())
	}
	return os.Truncate(path, offset)
}
