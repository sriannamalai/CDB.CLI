// Completion for the virtual filesystem paths and document fields the
// commands in this package take. It is the other half of navigate.go: cd, ls
// and info walk the path grammar, and these walk the same grammar one
// half-typed segment at a time.
package command

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// completePath keeps the short internal name every command already refers to.
func completePath(ctx context.Context, s *session.Session, args []string, cur string) []Candidate {
	return CompletePath(ctx, s, args, cur)
}

// completionPageSize bounds a completion lookup. Completion runs while the
// line editor owns the terminal, so it asks for one small page and never
// pages further.
const completionPageSize = 50

// idSentinel is the highest code point CouchDB sorts document ids against, so
// "prefix" to "prefix\ufff0" is the whole prefix range. It is what turns a
// startkey/endkey pair into a prefix query, with no skip involved.
const idSentinel = "\ufff0"

// CompletePath completes database, document, design-document and view names
// for a virtual path prefix.
func CompletePath(ctx context.Context, s *session.Session, _ []string, cur string) []Candidate {
	if !s.Connected() {
		return nil
	}
	// Split the prefix into the directory part that already exists and the
	// partial last segment being typed.
	dir, partial := splitPathPrefix(cur)
	// The line carries percent-encoded segments — path.Encode is what put them
	// there — while CouchDB keys, and the names it hands back, are raw text.
	// Every comparison and key bound below therefore works on the decoded
	// prefix, and only the candidate values are encoded again. A half-typed
	// escape ("%" with nothing after it) does not decode; the raw text is the
	// best guess then.
	if decoded, err := path.Decode(partial); err == nil {
		partial = decoded
	}
	join := func(name string) string {
		if dir == "" {
			return name
		}
		if strings.HasSuffix(dir, "/") {
			return dir + name
		}
		return dir + "/" + name
	}

	// "/db/_design/app/_view/" is not a resolvable path on its own, so trim the
	// trailing _view/ and complete view names against the design document.
	if trimmed := strings.TrimSuffix(dir, "_view/"); trimmed != dir {
		base, err := resolveDesignDoc(s.Path(), trimmed)
		if err != nil {
			return nil
		}
		return viewCandidates(ctx, s, base, partial, func(n string) string { return dir + path.Encode(n) })
	}

	base, err := path.Resolve(s.Path(), dir)
	if err != nil {
		// "/db/_partition/p1/_design/app/" does not resolve on its own — a
		// design document is not partition-scoped — but it is the prefix of a
		// partitioned view path, which does, so completion still has to reach
		// the design document behind it.
		base, err = resolveDesignDoc(s.Path(), dir)
		if err != nil {
			return nil
		}
	}
	switch base.Kind {
	case path.KindServer:
		names, err := s.Cache().Databases(ctx, s.Client)
		if err != nil {
			return nil
		}
		var out []Candidate
		for _, n := range names {
			if strings.HasPrefix(n, partial) {
				out = append(out, Candidate{Value: join(path.Encode(n)), Display: n, Tag: "databases"})
			}
		}
		return out

	case path.KindDatabase, path.KindPartition:
		var out []Candidate
		// A design document is not partition-scoped, so the partitioned
		// _all_docs never returns one. The segment is offered explicitly, the
		// same way _view is offered under a design document, so a partitioned
		// view path can be completed from the partition down.
		if base.Kind == path.KindPartition && partial != "_design" && strings.HasPrefix("_design", partial) {
			out = append(out, Candidate{Value: join("_design"), Display: "_design", Tag: "views"})
		}
		// Inside a partition CouchDB's keys are the fully qualified ids
		// ("p1:doc1") while the operator types the short form, so the key
		// range is qualified on the way in and the prefix is stripped on the
		// way out. Both halves go through path's own helpers, which is what
		// makes an id pasted with its prefix already on it complete to the
		// same document the short form names.
		prefix := partial
		if base.Partition != "" {
			prefix = path.PartitionDocID(base.Partition, partial)
		}
		page, err := s.Client.AllDocs(ctx, base.Database, couch.AllDocsOptions{
			Partition:     base.Partition,
			Limit:         completionPageSize,
			StartKeyDocID: prefix,
			EndKeyDocID:   prefix + idSentinel,
		})
		if err != nil {
			return out
		}
		for _, r := range page.Rows {
			if !strings.HasPrefix(r.ID, prefix) {
				continue
			}
			name := r.ID
			if base.Partition != "" {
				name = path.TrimPartition(base.Partition, name)
			}
			out = append(out, Candidate{Value: join(encodeDocID(name)), Display: name, Description: r.Rev, Tag: "documents"})
		}
		return out

	case path.KindDesignDoc:
		// Offer the _view segment before the view names themselves.
		if partial != "_view" && strings.HasPrefix("_view", partial) {
			return []Candidate{{Value: join("_view"), Display: "_view", Tag: "views"}}
		}
		return viewCandidates(ctx, s, base, partial, join)

	default:
		return nil
	}
}

// viewCandidates lists the view names of a design document that start with
// partial, mapping each through join to build the completion value.
func viewCandidates(ctx context.Context, s *session.Session, base path.Target, partial string, join func(string) string) []Candidate {
	raw, err := s.Client.DesignDoc(ctx, base.Database, base.DocID)
	if err != nil {
		return nil
	}
	var ddoc struct {
		Views map[string]json.RawMessage `json:"views"`
	}
	if err := json.Unmarshal(raw, &ddoc); err != nil {
		return nil
	}
	names := make([]string, 0, len(ddoc.Views))
	for n := range ddoc.Views {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Candidate
	for _, n := range names {
		if strings.HasPrefix(n, partial) {
			out = append(out, Candidate{Value: join(path.Encode(n)), Display: n, Tag: "views"})
		}
	}
	return out
}

// resolveDesignDoc resolves a path that names a design document, including the
// partition-scoped prefix "/db/_partition/p1/_design/app". That prefix is not
// a resolvable path — a design document is not partition-scoped — but it is
// the prefix of "/db/_partition/p1/_design/app/_view/<name>", which is, so it
// is resolved by asking for the view form and dropping the view.
func resolveDesignDoc(cwd, p string) (path.Target, error) {
	t, err := path.Resolve(cwd, p)
	if err == nil {
		if t.Kind != path.KindDesignDoc {
			return path.Target{}, &path.Error{Input: p, Reason: "not a design document"}
		}
		return t, nil
	}
	probe, perr := path.Resolve(cwd, strings.TrimRight(p, "/")+"/_view/x")
	if perr != nil || probe.Kind != path.KindView {
		return path.Target{}, err
	}
	probe.Kind, probe.View = path.KindDesignDoc, ""
	probe.Path = strings.TrimRight(p, "/")
	return probe, nil
}

// encodeDocID escapes a document id for use as the tail of a virtual path. A
// design document keeps its "_design/" prefix as a real separator: the rest of
// cdb speaks /db/_design/app, and percent-escaping the slash would produce
// /db/_design%2Fapp, which resolves to a plain document instead.
func encodeDocID(id string) string {
	const designPrefix = "_design/"
	if strings.HasPrefix(id, designPrefix) {
		return designPrefix + path.Encode(strings.TrimPrefix(id, designPrefix))
	}
	return path.Encode(id)
}

// splitPathPrefix separates the settled directory part of a path prefix from
// the partial segment the cursor is inside.
func splitPathPrefix(cur string) (dir, partial string) {
	i := strings.LastIndex(cur, "/")
	if i < 0 {
		return "", cur
	}
	return cur[:i+1], cur[i+1:]
}

// positionalArgs drops flag words, and the separate word a non-boolean flag
// consumes, from a partially typed argument list. Without it the "5" in
// "find --limit 5 /mydb" would be read as a path.
func positionalArgs(fs *pflag.FlagSet, args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i+1:]...)
		case strings.HasPrefix(a, "--"):
			name, _, attached := strings.Cut(a[2:], "=")
			if !attached && takesValue(fs.Lookup(name)) {
				i++
			}
		case strings.HasPrefix(a, "-") && a != "-":
			// Only a lone shorthand takes the next word: in a cluster, or with
			// anything attached, pflag reads the value out of the word itself.
			body, _, attached := strings.Cut(a[1:], "=")
			if !attached && len(body) == 1 && takesValue(fs.ShorthandLookup(body)) {
				i++
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

// takesValue reports whether a flag consumes the word after it. Boolean flags
// do not; every other type does. An unknown flag is treated as boolean, the
// safer guess while the line is still being typed: it leaves the next word
// available as a path instead of swallowing it.
func takesValue(f *pflag.Flag) bool {
	return f != nil && f.Value.Type() != "bool"
}

// CompleteFields completes document field names sampled from the database the
// session or one of the arguments points at.
func CompleteFields(ctx context.Context, s *session.Session, args []string, cur string) []Candidate {
	if !s.Connected() {
		return nil
	}
	target, err := s.Resolve("")
	if err != nil {
		return nil
	}
	// find is the only command that completes field names, so its own flag set
	// is what says which of the typed words are flag values rather than paths.
	for _, a := range positionalArgs(NewFlagSet(Find()), args) {
		if t, terr := s.Resolve(a); terr == nil && t.Database != "" {
			target = t
			break
		}
	}
	if target.Database == "" {
		return nil
	}
	fields, err := s.Cache().Fields(ctx, s.Client, target.Database)
	if err != nil {
		return nil
	}
	// --fields and --sort are StringSlice flags, so "title,ye" is one word.
	// Everything up to and including the last comma is already settled; only
	// the remainder is matched, and each candidate carries the settled part
	// back so accepting one does not throw the earlier fields away.
	prefix, match := "", cur
	if i := strings.LastIndex(cur, ","); i >= 0 {
		prefix, match = cur[:i+1], cur[i+1:]
	}
	var out []Candidate
	for _, f := range fields {
		if strings.HasPrefix(f, match) {
			out = append(out, Candidate{Value: prefix + f, Display: f, Tag: "fields"})
		}
	}
	return out
}
