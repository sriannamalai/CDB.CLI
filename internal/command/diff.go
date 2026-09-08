package command

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ChangeKind says how one field of a revision differs from the winner.
type ChangeKind int

const (
	// ChangeAdded: the field is present only in the revision.
	ChangeAdded ChangeKind = iota
	// ChangeRemoved: the field is present only in the winner.
	ChangeRemoved
	// ChangeChanged: the field is in both, with different values.
	ChangeChanged
)

// FieldChange is one top-level field that differs between a revision and the
// winner. Winner is empty when the field was added, Other when it was removed;
// both are value summaries, not raw JSON.
type FieldChange struct {
	Field  string
	Kind   ChangeKind
	Winner string
	Other  string
}

// RevisionDiff is one revision compared against the winner. Changes is empty
// for the winner itself.
type RevisionDiff struct {
	Rev     string
	Changes []FieldChange
	// notObject records a body that is not a JSON object. CouchDB will not
	// produce one, but a hand-written _bulk_docs can, and such a revision is
	// still a revision the operator may choose to keep — so it is described
	// rather than refused.
	notObject bool
}

// diffValueRunes is how much of a value a summary shows. A conflict chooser is
// a list of one-line entries; a whole field value would push the next entry off
// the screen.
const diffValueRunes = 40

// DiffRevisions compares a revision against the winning one at the top level.
//
// Fields whose names begin with "_" are CouchDB's own bookkeeping and are
// ignored, except _deleted, which is the whole content of a tombstone, and
// _attachments, which is compared by attachment count rather than by its
// stubs' digests. Comparison is on the marshalled form of the decoded value,
// so key order and whitespace do not register as a difference.
func DiffRevisions(winner, other json.RawMessage, rev string) RevisionDiff {
	d := RevisionDiff{Rev: rev}
	w, wok := decodeObject(winner)
	o, ook := decodeObject(other)
	if !wok || !ook {
		d.notObject = true
		return d
	}
	names := map[string]struct{}{}
	for k := range w {
		names[k] = struct{}{}
	}
	for k := range o {
		names[k] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		if strings.HasPrefix(k, "_") && k != "_deleted" && k != "_attachments" {
			continue
		}
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, field := range sorted {
		wv, inW := w[field]
		ov, inO := o[field]
		ws, os := diffValue(field, wv), diffValue(field, ov)
		switch {
		case inW && inO && ws == os:
			// Identical; nothing to report.
		case inW && inO:
			d.Changes = append(d.Changes, FieldChange{Field: field, Kind: ChangeChanged, Winner: ws, Other: os})
		case inO:
			d.Changes = append(d.Changes, FieldChange{Field: field, Kind: ChangeAdded, Other: os})
		default:
			d.Changes = append(d.Changes, FieldChange{Field: field, Kind: ChangeRemoved, Winner: ws})
		}
	}
	return d
}

// Summary renders the diff as one line for the resolve chooser.
func (d RevisionDiff) Summary() string {
	if d.notObject {
		return "this revision is not a JSON object"
	}
	if len(d.Changes) == 0 {
		return "no differences from the current revision"
	}
	var segments []string
	// Changed first, then added, then removed: what the two revisions disagree
	// about matters more than what only one of them has.
	for _, kind := range []ChangeKind{ChangeChanged, ChangeAdded, ChangeRemoved} {
		for _, c := range d.Changes {
			if c.Kind != kind {
				continue
			}
			switch kind {
			case ChangeChanged:
				segments = append(segments, fmt.Sprintf("changed: %s %s → %s", c.Field, c.Winner, c.Other))
			case ChangeAdded:
				segments = append(segments, fmt.Sprintf("added: %s %s", c.Field, c.Other))
			case ChangeRemoved:
				segments = append(segments, "removed: "+c.Field)
			}
		}
	}
	return strings.Join(segments, "; ")
}

// decodeObject decodes a body into a top-level map, reporting whether it was a
// JSON object at all.
func decodeObject(body json.RawMessage) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// diffValue summarises one field value. _attachments is rendered by count: its
// stubs carry digests and lengths that say nothing an operator choosing a
// revision needs.
func diffValue(field string, v any) string {
	if v == nil {
		return ""
	}
	if field == "_attachments" {
		if atts, ok := v.(map[string]any); ok {
			return fmt.Sprintf("%d attachment(s)", len(atts))
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	r := []rune(string(b))
	if len(r) > diffValueRunes {
		return string(r[:diffValueRunes-1]) + "…"
	}
	return string(r)
}
