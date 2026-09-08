package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Conflicts returns the conflicts command.
func Conflicts() Command {
	return Command{
		Name:        "conflicts",
		Summary:     "List conflicted documents, or the conflicting revisions of one document",
		Usage:       "[path]",
		MinArgs:     0,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.Int("limit", 100, "documents to scan per page")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			switch t.Kind {
			case path.KindDatabase, path.KindPartition:
				return conflictsInDatabase(ctx, s, t, inv.Int("limit"))
			case path.KindDocument, path.KindDesignDoc:
				winnerBody, winner, revs, err := conflictRevisions(ctx, s, t)
				if err != nil {
					return nil, err
				}
				if len(revs) == 0 {
					return Message{Text: fmt.Sprintf("%s has no conflicts. The current revision is %s.", t.Path, winner)}, nil
				}
				return conflictRows(ctx, s, t, winnerBody, winner, revs)
			default:
				return nil, Usagef("conflicts", "%s is a %s; conflicts works on a database or a document", t.Path, t.Kind)
			}
		},
	}
}

func conflictsInDatabase(ctx context.Context, s *session.Session, t path.Target, limit int) (Result, error) {
	rows := Rows{Columns: []Column{{Title: "id"}, {Title: "rev"}, {Title: "conflicts", Align: AlignRight}}}
	err := s.Client.AllDocsStream(ctx, t.Database, couch.AllDocsOptions{
		Partition:   t.Partition,
		Limit:       limit,
		IncludeDocs: true,
		Conflicts:   true,
	}, func(r couch.DocRow) error {
		var doc struct {
			Conflicts []string `json:"_conflicts"`
		}
		if len(r.Doc) == 0 {
			return nil
		}
		if err := json.Unmarshal(r.Doc, &doc); err != nil {
			return nil
		}
		if len(doc.Conflicts) == 0 {
			return nil
		}
		rows.Items = append(rows.Items, Row{
			Cells: []string{r.ID, r.Rev, strconv.Itoa(len(doc.Conflicts))},
			JSON:  mustJSON(map[string]any{"id": r.ID, "rev": r.Rev, "conflicts": doc.Conflicts}),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(rows.Items) == 0 {
		return Message{Text: fmt.Sprintf("No conflicted documents in %q.", t.Database)}, nil
	}
	rows.Hint = "resolve one with: resolve <path>"
	return rows, nil
}

// conflictRevisions reads the current (winning) document, along with its
// _conflicts list. The winning body comes back too so callers that need to
// display it are not forced into a second round trip.
func conflictRevisions(ctx context.Context, s *session.Session, t path.Target) (winnerBody json.RawMessage, winnerRev string, conflicts []string, err error) {
	body, rev, err := s.Client.GetDocument(ctx, t.Database, t.DocID, couch.GetOptions{Conflicts: true})
	if err != nil {
		return nil, "", nil, err
	}
	var doc struct {
		Conflicts []string `json:"_conflicts"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, "", nil, err
	}
	return body, rev, doc.Conflicts, nil
}

// conflictRows renders the winner (already fetched by conflictRevisions) and
// each conflicting revision. Every conflicting revision is fetched with its
// own GET rather than one open_revs=all request: the bulk form returns
// multipart/mixed, which is a parser this command does not need.
func conflictRows(ctx context.Context, s *session.Session, t path.Target, winnerBody json.RawMessage, winnerRev string, conflicts []string) (Result, error) {
	rows := Rows{Columns: []Column{{Title: "rev"}, {Title: "role"}, {Title: "document"}}}
	rows.Items = append(rows.Items, Row{
		Cells: []string{winnerRev, "winner", summarise(winnerBody)},
		JSON:  mustJSON(map[string]any{"rev": winnerRev, "role": "winner", "doc": json.RawMessage(winnerBody)}),
	})
	for _, rev := range conflicts {
		body, _, err := s.Client.GetDocument(ctx, t.Database, t.DocID, couch.GetOptions{Rev: rev})
		if err != nil {
			return nil, err
		}
		rows.Items = append(rows.Items, Row{
			Cells: []string{rev, "conflict", summarise(body)},
			JSON:  mustJSON(map[string]any{"rev": rev, "role": "conflict", "doc": json.RawMessage(body)}),
		})
	}
	rows.Hint = fmt.Sprintf("keep one with: resolve %s --keep <rev>", t.Path)
	return rows, nil
}

// Resolve returns the resolve command.
func Resolve() Command {
	return Command{
		Name:        "resolve",
		Summary:     "Keep one revision of a conflicted document and delete the rest",
		Usage:       "<doc-path>",
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Destructive: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("keep", "", "revision to keep")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDocument && t.Kind != path.KindDesignDoc {
				return nil, Usagef("resolve", "%s is a %s; resolve works on one document", t.Path, t.Kind)
			}
			_, winner, conflicts, err := conflictRevisions(ctx, s, t)
			if err != nil {
				return nil, err
			}
			if len(conflicts) == 0 {
				return Message{Text: fmt.Sprintf("%s has no conflicts. Nothing to do.", t.Path)}, nil
			}
			all := append([]string{winner}, conflicts...)
			keep := inv.String("keep")
			if keep == "" {
				keep, err = chooseRevision(ctx, s, t, all)
				if err != nil {
					return nil, err
				}
			}
			if !contains(all, keep) {
				return nil, Usagef("resolve", "%s is not one of the revisions of %s; they are %s", keep, t.Path, strings.Join(all, ", "))
			}
			var drop []string
			for _, r := range all {
				if r != keep {
					drop = append(drop, r)
				}
			}
			if err := Confirm(s, fmt.Sprintf("Keep %s and delete %d other revision(s) of %s?", keep, len(drop), t.Path)); err != nil {
				return nil, err
			}
			for _, r := range drop {
				if _, err := s.Client.DeleteDocument(ctx, t.Database, t.DocID, r); err != nil {
					return nil, err
				}
			}
			return Message{Text: fmt.Sprintf("Kept revision %s of %s and deleted %d other revision(s).", keep, t.Path, len(drop))}, nil
		},
	}
}

// chooseRevision shows each revision and asks which to keep.
func chooseRevision(ctx context.Context, s *session.Session, t path.Target, revs []string) (string, error) {
	if !s.Prefs.Interactive {
		return "", Usagef("resolve", "%s has %d revisions; pass --keep <rev> to choose one", t.Path, len(revs))
	}
	for i, rev := range revs {
		body, _, err := s.Client.GetDocument(ctx, t.Database, t.DocID, couch.GetOptions{Rev: rev})
		if err != nil {
			return "", err
		}
		role := "conflict"
		if i == 0 {
			role = "current"
		}
		fmt.Fprintf(s.Stdout, "%d) %s (%s)\n   %s\n", i+1, rev, role, summarise(body))
	}
	fmt.Fprintf(s.Stdout, "Keep which revision? [1-%d] ", len(revs))
	line, err := s.Reader().ReadString('\n')
	if err != nil && line == "" {
		return "", ErrDeclined
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(revs) {
		return "", Usagef("resolve", "expected a number between 1 and %d", len(revs))
	}
	return revs[n-1], nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
