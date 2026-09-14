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
