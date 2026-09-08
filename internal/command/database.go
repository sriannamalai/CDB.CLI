package command

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Mkdir returns the mkdir command.
func Mkdir() Command {
	return Command{
		Name:        "mkdir",
		Summary:     "Create a database",
		Usage:       "<path>",
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("partitioned", false, "create a partitioned database")
			fs.Int("q", 0, "shard count, or 0 for the server default")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDatabase {
				return nil, Usagef("mkdir", "%s is a %s; mkdir creates databases, as in \"mkdir /mydb\"", t.Path, t.Kind)
			}
			if err := s.Client.CreateDatabase(ctx, t.Database, inv.Bool("partitioned"), inv.Int("q")); err != nil {
				return nil, err
			}
			return Message{Text: fmt.Sprintf("Created database %q.", t.Database)}, nil
		},
	}
}

// Rmdir returns the rmdir command.
func Rmdir() Command {
	return Command{
		Name:        "rmdir",
		Summary:     "Delete a database and everything in it",
		Usage:       "<path>",
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Destructive: true,
		Complete:    completePath,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDatabase {
				return nil, Usagef("rmdir", "%s is a %s; rmdir deletes databases, as in \"rmdir /mydb\"", t.Path, t.Kind)
			}
			info, err := s.Client.DatabaseInfo(ctx, t.Database)
			if err != nil {
				return nil, err
			}
			prompt := fmt.Sprintf("This deletes %q and its %d document(s) permanently. Type the database name to confirm", t.Database, info.DocCount)
			if err := ConfirmPhrase(s, prompt, t.Database); err != nil {
				return nil, err
			}
			if err := s.Client.DestroyDatabase(ctx, t.Database); err != nil {
				return nil, err
			}
			// Match the database itself or something inside it, never a
			// sibling whose name merely starts with the same text: deleting
			// /mydb must not move you out of /mydb2.
			if s.Path() == t.Path || strings.HasPrefix(s.Path(), t.Path+"/") {
				s.SetPath("/")
			}
			return Message{Text: fmt.Sprintf("Deleted database %q.", t.Database)}, nil
		},
	}
}

// Cp returns the cp command. Two documents are copied with a CouchDB COPY; two
// databases start a one-shot replication.
func Cp() Command {
	return Command{
		Name:        "cp",
		Summary:     "Copy a document, or replicate one database into another",
		Usage:       "<source> <destination>",
		MinArgs:     2,
		MaxArgs:     2,
		NeedsClient: true,
		Complete:    completePath,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			src, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			dst, err := s.Resolve(inv.Arg(1))
			if err != nil {
				return nil, err
			}
			switch {
			case src.Kind == path.KindDatabase && dst.Kind == path.KindDatabase:
				return copyDatabase(ctx, s, src, dst)
			case (src.Kind == path.KindDocument || src.Kind == path.KindDesignDoc) &&
				(dst.Kind == path.KindDocument || dst.Kind == path.KindDesignDoc):
				if src.Database != dst.Database {
					return nil, Usagef("cp", "COPY only works inside one database; use \"cat %s | put %s\" to move a document between databases", src.Path, dst.Path)
				}
				dstRev, err := s.Client.GetRev(ctx, dst.Database, dst.DocID)
				if err != nil {
					// A missing destination is the create case, not a failure:
					// CopyDocument is then told to create rather than overwrite.
					if ce, ok := couch.AsError(err); !ok || ce.Status != 404 {
						return nil, err
					}
					dstRev = ""
				}
				rev, err := s.Client.CopyDocument(ctx, src.Database, src.DocID, dst.DocID, dstRev)
				if err != nil {
					return nil, err
				}
				return Message{Text: fmt.Sprintf("Copied %s to %s at revision %s.", src.Path, dst.Path, rev)}, nil
			default:
				return nil, Usagef("cp", "cp copies a document to a document, or a database to a database; got a %s and a %s", src.Kind, dst.Kind)
			}
		},
	}
}

// copyDatabase writes a one-shot _replicator document. Task 20 replaces the
// body of this function with a call to replicate.Create.
//
// source and target are the bare, encoded database names, not full URLs.
// Client.URL() is deliberately stripped of userinfo (it is safe to print,
// log, or put in an error message), so building a URL from it here would
// hand the replicator a remote HTTP endpoint with no credentials attached; on
// any authenticated server the replication then fails asynchronously with a
// 401 that "cp" never sees, while still reporting success. A bare name tells
// CouchDB to replicate the local database directly, with no credentials in
// the document at all.
func copyDatabase(ctx context.Context, s *session.Session, src, dst path.Target) (Result, error) {
	source := path.Encode(src.Database)
	target := path.Encode(dst.Database)
	doc := map[string]any{
		"source":        source,
		"target":        target,
		"create_target": true,
		"continuous":    false,
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := s.Client.DoJSON(ctx, "POST", "/_replicator", doc, &out, "create", "replication"); err != nil {
		return nil, err
	}
	return Message{Text: fmt.Sprintf("Started replication %s from %s to %s. Run \"cdb replications\" to watch it.", out.ID, src.Path, dst.Path)}, nil
}
