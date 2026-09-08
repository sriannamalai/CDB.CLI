package render

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/alecthomas/chroma/v2/quick"
)

// jsonStyle is the chroma style used for highlighted JSON.
const jsonStyle = "monokai"

// CompactJSON removes insignificant whitespace.
func CompactJSON(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, src); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// prettyJSON indents with two spaces.
func prettyJSON(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, src, "", "  "); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// HighlightJSON writes src to dst, pretty-printed, with chroma colouring when
// color is true. Input that is not valid JSON is written through unchanged.
func HighlightJSON(dst io.Writer, src []byte, color bool) error {
	pretty, err := prettyJSON(src)
	if err != nil {
		pretty = src
	}
	if !color {
		if _, err := dst.Write(append(pretty, '\n')); err != nil {
			return err
		}
		return nil
	}
	if err := quick.Highlight(dst, string(pretty), "json", "terminal256", jsonStyle); err != nil {
		_, werr := dst.Write(append(pretty, '\n'))
		return werr
	}
	_, err = dst.Write([]byte{'\n'})
	return err
}
