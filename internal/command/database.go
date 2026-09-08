package command

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/replicate"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Mkdir returns the mkdir command.
func Mkdir() Command {
	return Command{
		Name:    "mkdir",
		Summary: "Create a database",
		Example: `$ cdb mkdir /movies
Created database "movies".

$ cdb mkdir /events --partitioned --q 4`,
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
				return nil, Usagef("mkdir", "%s is %s %s; mkdir creates databases, as in \"mkdir /mydb\"", t.Path, t.Kind.Article(), t.Kind)
			}
			if err := s.Client.CreateDatabase(ctx, t.Database, inv.Bool("partitioned"), inv.Int("q")); err != nil {
				return nil, err
			}
			// Completion caches the database list for the whole session, so the
			// new database has to be announced to it.
			s.Cache().InvalidateDatabases()
			return Message{Text: fmt.Sprintf("Created database %q.", t.Database)}, nil
		},
	}
}

// Rmdir returns the rmdir command.
func Rmdir() Command {
	return Command{
		Name:    "rmdir",
		Summary: "Delete a database and everything in it",
		Example: `$ cdb rmdir /movies-copy --yes
Deleted database "movies-copy".`,
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
				return nil, Usagef("rmdir", "%s is %s %s; rmdir deletes databases, as in \"rmdir /mydb\"", t.Path, t.Kind.Article(), t.Kind)
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
			s.Cache().InvalidateDatabases()
			s.Cache().InvalidateFields(t.Database)
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
		Name:    "cp",
		Summary: "Copy a document, or replicate one database into another",
		Example: `$ cdb cp /movies/tt0211915 /movies/tt0211915-copy
Copied /movies/tt0211915 to /movies/tt0211915-copy at revision 1-5a1da49eff1181cb80527cce17097f40.

$ cdb cp /movies /movies-archive
Started replication ccaace0a9e8a2ddf328eaf46210031fc from /movies to /movies-archive. Run "cdb replications" to watch it.`,
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

// copyDatabase starts a one-shot replication from src to dst. It is the same
// replication "replicate" performs, and it goes through the same package, so
// that the endpoint rules live in exactly one place: CouchDB 3.x rejects a
// bare database name (403 local_endpoints_not_supported), so each endpoint is
// a full URL plus a per-endpoint credential object that never leaves the
// request body.
func copyDatabase(ctx context.Context, s *session.Session, src, dst path.Target) (Result, error) {
	replicationURL, err := replicationBase(s, "cp")
	if err != nil {
		return nil, err
	}
	source, err := replicate.ResolveEndpointFor(s.Client, replicationURL, s.Path(), src.Path)
	if err != nil {
		return nil, Usagef("cp", "%v", err)
	}
	target, err := replicate.ResolveEndpointFor(s.Client, replicationURL, s.Path(), dst.Path)
	if err != nil {
		return nil, Usagef("cp", "%v", err)
	}
	id, err := replicate.Create(ctx, s.Client, replicate.Request{
		Source:       source,
		Target:       target,
		CreateTarget: true,
	})
	if err != nil {
		return nil, err
	}
	return Message{Text: fmt.Sprintf("Started replication %s from %s to %s. Run \"cdb replications\" to watch it.", id, src.Path, dst.Path)}, nil
}
