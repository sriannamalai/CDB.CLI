package command

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// couchBulkResult is couch.BulkResult, aliased so that the long Stream
// literals below read as one line each.
type couchBulkResult = couch.BulkResult

// pipeBatch is how many documents a piped consumer sends per request. Spec
// section 4 fixes it at 100 for put, rm and cat alike.
const pipeBatch = 100

// pipeDatabase is the database a piped consumer acts on: the path it was given
// when there is one, and otherwise the database the current directory is in.
func pipeDatabase(s *session.Session, name, arg string) (string, error) {
	target := arg
	if target == "" {
		target = s.Path()
	}
	t, err := s.Resolve(target)
	if err != nil {
		return "", err
	}
	if t.Database == "" {
		return "", &UsageError{Reason: fmt.Sprintf("%s needs a database path when the current directory is /", name)}
	}
	if t.Kind != path.KindDatabase {
		return "", &UsageError{Reason: fmt.Sprintf("%s reads a pipeline into a database; %s is %s %s", name, t.Path, t.Kind.Article(), t.Kind)}
	}
	return t.Database, nil
}

// nextBatch reads up to pipeBatch values off the pipe. It waits for the first
// value and then takes only what is already queued, so a live source's one
// change is acted on now rather than held back until ninety-nine more arrive.
// more is false when the pipeline has finished and the batch is empty. seen
// counts every value read, because the sentences name a value by its position
// in the whole pipeline, not in its batch.
func nextBatch(ctx context.Context, p *Pipe, seen *int, check func(n int, v json.RawMessage) (json.RawMessage, error)) ([]json.RawMessage, bool, error) {
	batch := make([]json.RawMessage, 0, pipeBatch)
	for len(batch) < pipeBatch {
		var (
			v   json.RawMessage
			ok  bool
			err error
		)
		if len(batch) == 0 {
			v, ok, err = p.Next(ctx)
			if err != nil {
				return nil, false, err
			}
		} else {
			var waiting bool
			v, ok, waiting = p.TryNext()
			if !waiting {
				break
			}
		}
		if !ok {
			break
		}
		*seen++
		out, cerr := check(*seen, v)
		if cerr != nil {
			return nil, false, cerr
		}
		batch = append(batch, out)
	}
	return batch, len(batch) > 0, nil
}

// requireObject checks that a piped value is a JSON object, the only thing
// _bulk_docs can be given.
func requireObject(n int, v json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(v, &obj); err != nil || obj == nil {
		return nil, Errorf(nil, "value %d is not a JSON object", n)
	}
	return v, nil
}

// dropAndResolveRevs strips any top-level "_rev" a pipeline document carries
// and, for the documents in the batch that name an "_id", looks up the
// target's current revisions in one _all_docs request and writes over
// whichever ones it already holds. put's pipeline decides create vs. update
// for itself from the target database, never from a revision a document
// happened to arrive with — one copied from another database, or echoed back
// from an earlier put row — which is what lets a pipeline copy and update
// without conflicting and without asking to confirm an overwrite.
//
// A deleted target document looks exactly like a missing one here: _all_docs
// never lists a deleted document, so its key comes back not_found and the
// incoming document is written as a create, same as an id the target never
// held.
func dropAndResolveRevs(ctx context.Context, s *session.Session, db string, batch []json.RawMessage) ([]json.RawMessage, error) {
	docs := make([]map[string]json.RawMessage, len(batch))
	var ids []string
	indexByID := make(map[string]int, len(batch))
	for i, v := range batch {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(v, &m); err != nil {
			return nil, err
		}
		delete(m, "_rev")
		docs[i] = m
		if raw, ok := m["_id"]; ok {
			var id string
			if json.Unmarshal(raw, &id) == nil && id != "" {
				ids = append(ids, id)
				indexByID[id] = i
			}
		}
	}
	if len(ids) > 0 {
		rows, err := s.Client.AllDocsByKeys(ctx, db, ids, false)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.Error != "" || r.Rev == "" {
				continue
			}
			i, ok := indexByID[r.ID]
			if !ok {
				continue
			}
			quoted, err := json.Marshal(r.Rev)
			if err != nil {
				return nil, err
			}
			docs[i]["_rev"] = quoted
		}
	}
	out := make([]json.RawMessage, len(docs))
	for i, m := range docs {
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// putPipeline writes every document of a pipeline through _bulk_docs, in
// batches, and reports one row per document as each batch comes back.
//
// The result is a live Stream rather than Rows because the source above may be
// a feed with no end: a result collected before it is printed would show a
// "tail --follow | put" nothing at all.
func putPipeline(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
	db, err := pipeDatabase(s, "put", inv.Arg(0))
	if err != nil {
		return nil, err
	}
	var (
		seen    int
		pending []couchBulkResult
		done    bool
	)
	return Stream{
		Live:    true,
		Columns: []Column{{Title: "id"}, {Title: "rev"}, {Title: "status"}},
		Next: func() (Row, bool, error) {
			for len(pending) == 0 {
				if done {
					return Row{}, false, nil
				}
				batch, more, err := nextBatch(ctx, inv.Pipe, &seen, requireObject)
				if err != nil {
					return Row{}, false, err
				}
				if !more {
					done = true
					return Row{}, false, nil
				}
				resolved, err := dropAndResolveRevs(ctx, s, db, batch)
				if err != nil {
					return Row{}, false, err
				}
				if pending, err = s.Client.BulkWrite(ctx, db, resolved); err != nil {
					return Row{}, false, err
				}
			}
			r := pending[0]
			pending = pending[1:]
			return Row{
				Cells: []string{r.ID, r.Rev, r.Status},
				JSON:  jsonObject("id", r.ID, "rev", r.Rev, "status", r.Status),
			}, true, nil
		},
	}, nil
}

// ref is one reference read off a pipeline, resolved against the stage's own
// database. Rev is set only when the reference carried one.
type ref struct {
	DB  string
	ID  string
	Rev string
}

// referenceRev reads a revision out of a piped value, for the two shapes that
// carry one: a document's own "_rev", and the "value":{"rev":…} of an _all_docs
// or view row. It returns "" when the value names none, and the consumer looks
// the current revision up.
func referenceRev(v json.RawMessage) string {
	var obj struct {
		Rev   string `json:"_rev"`
		Value struct {
			Rev string `json:"rev"`
		} `json:"value"`
	}
	if err := json.Unmarshal(v, &obj); err != nil {
		return ""
	}
	if obj.Rev != "" {
		return obj.Rev
	}
	return obj.Value.Rev
}

// nextRefs reads up to pipeBatch references off the pipe, each defaulting to
// db unless it named its own database with an absolute "/db/id" path.
func nextRefs(ctx context.Context, p *Pipe, seen *int, db string) ([]ref, bool, error) {
	var refs []ref
	_, more, err := nextBatch(ctx, p, seen, func(n int, v json.RawMessage) (json.RawMessage, error) {
		refDB, id, rerr := ParseReference(v)
		if rerr != nil {
			return nil, Errorf(nil, "value %d is not a document reference", n)
		}
		if refDB == "" {
			refDB = db
		}
		refs = append(refs, ref{DB: refDB, ID: id, Rev: referenceRev(v)})
		return v, nil
	})
	if err != nil {
		return nil, false, err
	}
	return refs, more, nil
}

// byDatabase groups a batch of references, keeping the databases in the order
// they first appeared so that the rows come back in pipeline order as far as
// one batch can preserve it.
func byDatabase(refs []ref) ([]string, map[string][]ref) {
	var order []string
	groups := map[string][]ref{}
	for _, r := range refs {
		if _, seen := groups[r.DB]; !seen {
			order = append(order, r.DB)
		}
		groups[r.DB] = append(groups[r.DB], r)
	}
	return order, groups
}

// fillRevisions looks up the current revision of every reference in one
// database that did not carry one. A reference the database does not hold is
// left with an empty Rev, which rmBatch reports as "not_found".
func fillRevisions(ctx context.Context, s *session.Session, db string, refs []ref) ([]ref, error) {
	var keys []string
	for _, r := range refs {
		if r.Rev == "" {
			keys = append(keys, r.ID)
		}
	}
	if len(keys) == 0 {
		return refs, nil
	}
	rows, err := s.Client.AllDocsByKeys(ctx, db, keys, false)
	if err != nil {
		return nil, err
	}
	revs := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Error == "" {
			revs[row.ID] = row.Rev
		}
	}
	out := make([]ref, len(refs))
	copy(out, refs)
	for i := range out {
		if out[i].Rev == "" {
			out[i].Rev = revs[out[i].ID]
		}
	}
	return out, nil
}

// rmBatch deletes one database's worth of references and returns one result
// per reference, in the order they were given. A reference with no revision is
// one the database does not hold, and is reported rather than sent.
func rmBatch(ctx context.Context, s *session.Session, db string, refs []ref) ([]couchBulkResult, error) {
	refs, err := fillRevisions(ctx, s, db, refs)
	if err != nil {
		return nil, err
	}
	var (
		docs []json.RawMessage
		out  []couchBulkResult
		// sent[i] is the row the i-th document written belongs to. _bulk_docs
		// answers in the order it was asked, which is the only way to tell two
		// rows for the same id apart: a view emits several keys per document,
		// so one batch can carry the same id twice, and every tombstone after
		// the first conflicts. Keyed by id instead, the later result would
		// overwrite one row and leave the others reporting an "ok" that never
		// happened.
		sent []int
	)
	for _, r := range refs {
		if r.Rev == "" {
			out = append(out, couchBulkResult{ID: r.ID, Status: "not_found"})
			continue
		}
		sent = append(sent, len(out))
		out = append(out, couchBulkResult{ID: r.ID, Rev: r.Rev, Status: "ok"})
		docs = append(docs, jsonDeleted(r.ID, r.Rev))
	}
	if len(docs) == 0 {
		return out, nil
	}
	written, err := s.Client.BulkWrite(ctx, db, docs)
	if err != nil {
		return nil, err
	}
	for i, w := range written {
		if i < len(sent) {
			out[sent[i]] = w
		}
	}
	return out, nil
}

// jsonDeleted is the tombstone _bulk_docs wants for a deletion.
func jsonDeleted(id, rev string) json.RawMessage {
	b, err := json.Marshal(map[string]any{"_id": id, "_rev": rev, "_deleted": true})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// rmPipeline deletes every document a pipeline names. It confirms once, before
// it reads anything, so a script needs --yes exactly once too.
func rmPipeline(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
	db, err := pipeDatabase(s, "rm", inv.Arg(0))
	if err != nil {
		return nil, err
	}
	if err := Confirm(ctx, s, fmt.Sprintf("Delete the piped documents from %s?", db)); err != nil {
		return nil, err
	}
	var (
		seen    int
		pending []couchBulkResult
		done    bool
	)
	return Stream{
		Live:    true,
		Columns: []Column{{Title: "id"}, {Title: "rev"}, {Title: "status"}},
		Next: func() (Row, bool, error) {
			for len(pending) == 0 {
				if done {
					return Row{}, false, nil
				}
				refs, more, err := nextRefs(ctx, inv.Pipe, &seen, db)
				if err != nil {
					return Row{}, false, err
				}
				if !more {
					done = true
					return Row{}, false, nil
				}
				order, groups := byDatabase(refs)
				for _, name := range order {
					res, err := rmBatch(ctx, s, name, groups[name])
					if err != nil {
						return Row{}, false, err
					}
					pending = append(pending, res...)
				}
			}
			r := pending[0]
			pending = pending[1:]
			return Row{
				Cells: []string{r.ID, r.Rev, r.Status},
				JSON:  jsonObject("id", r.ID, "rev", r.Rev, "status", r.Status),
			}, true, nil
		},
	}, nil
}

// catPipeline emits every document a pipeline names. An id the database does
// not hold ends the stage: "cat" was asked for a document, and there is none.
func catPipeline(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
	db, err := pipeDatabase(s, "cat", inv.Arg(0))
	if err != nil {
		return nil, err
	}
	var (
		seen    int
		pending []json.RawMessage
		done    bool
	)
	return Stream{
		Live:    true,
		Columns: []Column{{Title: "document"}},
		Next: func() (Row, bool, error) {
			for len(pending) == 0 {
				if done {
					return Row{}, false, nil
				}
				refs, more, err := nextRefs(ctx, inv.Pipe, &seen, db)
				if err != nil {
					return Row{}, false, err
				}
				if !more {
					done = true
					return Row{}, false, nil
				}
				order, groups := byDatabase(refs)
				for _, name := range order {
					keys := make([]string, 0, len(groups[name]))
					for _, r := range groups[name] {
						keys = append(keys, r.ID)
					}
					rows, err := s.Client.AllDocsByKeys(ctx, name, keys, true)
					if err != nil {
						return Row{}, false, err
					}
					for _, row := range rows {
						if row.Error != "" || len(row.Doc) == 0 {
							return Row{}, false, Errorf(nil, "%q is not in %s", row.ID, name)
						}
						pending = append(pending, row.Doc)
					}
				}
			}
			doc := pending[0]
			pending = pending[1:]
			return Row{Cells: []string{string(doc)}, JSON: doc}, true, nil
		},
	}, nil
}
