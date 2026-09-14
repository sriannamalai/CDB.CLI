package command

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// securityDetails is the long help for security.
const securityDetails = `Every database has a _security document with two levels. A member may read and
write documents; an admin may also change design documents and the security
document itself. Each level lists names and roles, and a request is allowed if
either matches. A database whose members list is empty is readable by anyone
the server lets in at all.

With no flag, "security" prints the document. With one or more flags it reads
the document, applies every change, asks once, and writes it back in a single
PUT, so two operators editing different entries cannot lose each other's work
to a half-applied edit.`

// SecurityCmd returns the security command. The Go identifier is not Security
// because internal/couch already exports a type by that name and the file
// reads better without the collision.
func SecurityCmd() Command {
	return Command{
		Name:    "security",
		Summary: "Show and change who may read and write a database",
		Example: `$ cdb security /movies
 ROLE    | KIND  | VALUE
---------+-------+--------
 admins  | names | alice
 admins  | roles | (none)
 members | names | bob
 members | roles | reader

$ cdb security /movies --add-member carol --remove-member bob
Grant member access on movies to carol, revoke member access on movies from bob? [y/N] y
Changed the security of movies.`,
		Usage:       "<db-path>",
		Details:     securityDetails,
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringSlice("add-admin", nil, "give a user admin access")
			fs.StringSlice("remove-admin", nil, "take a user's admin access away")
			fs.StringSlice("add-admin-role", nil, "give a role admin access")
			fs.StringSlice("remove-admin-role", nil, "take a role's admin access away")
			fs.StringSlice("add-member", nil, "give a user member access")
			fs.StringSlice("remove-member", nil, "take a user's member access away")
			fs.StringSlice("add-member-role", nil, "give a role member access")
			fs.StringSlice("remove-member-role", nil, "take a role's member access away")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDatabase {
				return nil, Usagef("security", "%s is %s %s; security acts on a database, as in \"security /mydb\"",
					t.Path, t.Kind.Article(), t.Kind)
			}
			sec, err := s.Client.Security(ctx, t.Database)
			if err != nil {
				return nil, err
			}
			edits := securityEditsFrom(inv, t.Database)
			if len(edits) == 0 {
				return securityRows(sec), nil
			}
			changed := false
			for _, e := range edits {
				if e.apply(&sec) {
					changed = true
				}
			}
			if !changed {
				return Message{Text: fmt.Sprintf("The security of %s is already as asked; nothing was changed.", t.Database)}, nil
			}
			if err := Confirm(ctx, s, securityPrompt(edits)); err != nil {
				return nil, err
			}
			if err := s.Client.SetSecurity(ctx, t.Database, sec); err != nil {
				return nil, err
			}
			return Message{Text: fmt.Sprintf("Changed the security of %s.", t.Database)}, nil
		},
	}
}

// securityRows renders the document, one row per entry, with an explicit
// "(none)" where a list is empty — a blank line would read as a missing row
// rather than as a level nobody is on.
func securityRows(sec couch.Security) Rows {
	rows := Rows{Columns: []Column{{Title: "role"}, {Title: "kind"}, {Title: "value"}}}
	add := func(role, kind string, values []string) {
		if len(values) == 0 {
			rows.Items = append(rows.Items, securityRow(role, kind, "(none)"))
			return
		}
		sorted := append([]string(nil), values...)
		sort.Strings(sorted)
		for _, v := range sorted {
			rows.Items = append(rows.Items, securityRow(role, kind, v))
		}
	}
	add("admins", "names", sec.Admins.Names)
	add("admins", "roles", sec.Admins.Roles)
	add("members", "names", sec.Members.Names)
	add("members", "roles", sec.Members.Roles)
	return rows
}

func securityRow(role, kind, value string) Row {
	return Row{
		Cells: []string{role, kind, value},
		JSON:  jsonObject("role", role, "kind", kind, "value", value),
	}
}

// securityEdit is one requested change, with the sentence it contributes to the
// confirmation already written.
type securityEdit struct {
	clause string
	apply  func(sec *couch.Security) bool
}

// securityEditsFrom turns the eight flags into edits, in the order the flags
// are declared, so that the confirmation reads in a stable order whatever the
// operator typed.
func securityEditsFrom(inv Invocation, db string) []securityEdit {
	var edits []securityEdit
	for _, spec := range []struct {
		flag  string
		grant bool
		role  bool
		level string
		list  func(sec *couch.Security) *[]string
	}{
		{"add-admin", true, false, "admin", func(s *couch.Security) *[]string { return &s.Admins.Names }},
		{"remove-admin", false, false, "admin", func(s *couch.Security) *[]string { return &s.Admins.Names }},
		{"add-admin-role", true, true, "admin", func(s *couch.Security) *[]string { return &s.Admins.Roles }},
		{"remove-admin-role", false, true, "admin", func(s *couch.Security) *[]string { return &s.Admins.Roles }},
		{"add-member", true, false, "member", func(s *couch.Security) *[]string { return &s.Members.Names }},
		{"remove-member", false, false, "member", func(s *couch.Security) *[]string { return &s.Members.Names }},
		{"add-member-role", true, true, "member", func(s *couch.Security) *[]string { return &s.Members.Roles }},
		{"remove-member-role", false, true, "member", func(s *couch.Security) *[]string { return &s.Members.Roles }},
	} {
		for _, value := range inv.StringSlice(spec.flag) {
			value, spec := value, spec
			who := value
			if spec.role {
				who = "role " + value
			}
			clause := fmt.Sprintf("revoke %s access on %s from %s", spec.level, db, who)
			if spec.grant {
				clause = fmt.Sprintf("grant %s access on %s to %s", spec.level, db, who)
			}
			edits = append(edits, securityEdit{
				clause: clause,
				apply: func(sec *couch.Security) bool {
					list := spec.list(sec)
					if spec.grant {
						return appendUnique(list, value)
					}
					return removeValue(list, value)
				},
			})
		}
	}
	return edits
}

// appendUnique adds value unless it is already there, reporting whether the
// list changed.
func appendUnique(list *[]string, value string) bool {
	for _, v := range *list {
		if v == value {
			return false
		}
	}
	*list = append(*list, value)
	return true
}

// removeValue drops every occurrence of value, reporting whether the list
// changed.
func removeValue(list *[]string, value string) bool {
	out := (*list)[:0]
	changed := false
	for _, v := range *list {
		if v == value {
			changed = true
			continue
		}
		out = append(out, v)
	}
	*list = out
	return changed
}

// securityPrompt is the one question every edit in a call shares. A single
// change reads as a sentence of its own; several are joined so that the
// operator sees the whole edit before agreeing to any of it.
func securityPrompt(edits []securityEdit) string {
	clauses := make([]string, 0, len(edits))
	for _, e := range edits {
		clauses = append(clauses, e.clause)
	}
	joined := strings.Join(clauses, ", ")
	return strings.ToUpper(joined[:1]) + joined[1:] + "?"
}
