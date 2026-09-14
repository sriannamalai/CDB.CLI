package command

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// defaultNode is the node --node names when the operator does not. CouchDB
// documents "_local" as the alias for the node the request landed on.
//
// https://docs.couchdb.org/en/stable/api/server/configuration.html
const defaultNode = "_local"

// redactedValue is what a secret configuration value renders as.
const redactedValue = "****"

// redactedSections are the sections whose every key is a credential: [admins]
// holds password hashes, [jwt_keys] holds the keys that mint valid tokens.
var redactedSections = []string{"admins", "jwt_keys"}

// redactedKeys are the individual keys that are credentials wherever they
// appear. The shared proxy secret is spelled two ways because CouchDB reads
// both sections: [chttpd_auth] is current, [couch_httpd_auth] is the older
// name servers in the 3.0 era still honour.
var redactedKeys = map[string]bool{
	"chttpd_auth/secret":      true,
	"couch_httpd_auth/secret": true,
}

// redactedSuffixes catch the keys nobody has enumerated: a value whose key ends
// in one of these is a credential in every CouchDB section that has one, and in
// every section a later release adds.
var redactedSuffixes = []string{"password", "secret", "token"}

// unredactedKeys are the keys the suffix rule would catch but that hold no
// credential: proxy_use_secret is a boolean toggling whether the proxy auth
// handler requires X-Auth-CouchDB-Token at all, not the shared secret itself
// (that is chttpd_auth/secret, already caught above). Spelled twice for the
// same reason redactedKeys is: CouchDB still honours the pre-3.x section name.
var unredactedKeys = map[string]bool{
	"chttpd_auth/proxy_use_secret":      true,
	"couch_httpd_auth/proxy_use_secret": true,
}

// startupOnlyKeys are the settings CouchDB reads when it starts and does not
// re-read afterwards: writing one through _config changes what the next boot
// will use, and changes nothing about the running server. "config set" says so
// and asks first.
//
// The 3.5 configuration reference does not annotate these keys
// (https://docs.couchdb.org/en/stable/config/http.html,
// https://docs.couchdb.org/en/stable/config/couchdb.html — checked 2026-09-14),
// so the list rests on three things: CouchDB reads default.ini and local.ini at
// boot and _config/_reload re-reads those same files
// (https://docs.couchdb.org/en/stable/api/server/configuration.html); this
// repository's own CI writes [chttpd] authentication_handlers through _config
// and then restarts the container rather than reloading it
// (.github/workflows/ci.yml); and [couchdb] single_node is documented as
// creating the system databases "on startup". Being on this list costs an
// operator one sentence and one confirmation, so the list errs towards warning.
//
// A "*" key means every key in the section.
var startupOnlyKeys = map[string][]string{
	"chttpd":   {"port", "bind_address", "authentication_handlers"},
	"httpd":    {"port", "bind_address"},
	"couchdb":  {"database_dir", "view_index_dir", "uuid", "single_node"},
	"cluster":  {"n", "q"},
	"jwt_keys": {"*"},
	"ssl":      {"*"},
	"nouveau":  {"enable", "url"},
}

// startupOnly reports whether a key is read only when CouchDB starts.
func startupOnly(section, key string) bool {
	for _, k := range startupOnlyKeys[section] {
		if k == "*" || k == key {
			return true
		}
	}
	return false
}

// redactedConfigValue renders a configuration value, replacing a credential
// with "****" unless the operator asked to see it. It is the single place the
// rule lives: the table, --json and the "was …" clause of set and unset all
// call it, so none of them can drift.
func redactedConfigValue(section, key, value string, reveal bool) string {
	if reveal || value == "" {
		return value
	}
	for _, s := range redactedSections {
		if section == s {
			return redactedValue
		}
	}
	if redactedKeys[section+"/"+key] {
		return redactedValue
	}
	if unredactedKeys[section+"/"+key] {
		return value
	}
	lower := strings.ToLower(key)
	for _, suffix := range redactedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return redactedValue
		}
	}
	return value
}

// configDetails is the long help for config.
const configDetails = `"config" with no argument prints every setting of the node; with a section it
prints that section, and with <section>/<key> just that one value. Values that
are credentials — everything under [admins] and [jwt_keys], the shared proxy
secret, and any key whose name ends in password, secret or token — print as
**** unless --reveal is given. The same rule applies to --json.

Some settings are read only when CouchDB starts: the ports and bind addresses,
the authentication handler chain, the data directories, the cluster defaults,
the JWT keys, the TLS options and the Nouveau switches. Changing one of those
changes what the next start will use and nothing about the running server, so
"config set" says so and asks first.

"config reload" makes the node re-read its ini files, discarding any change
made through this command that was never written to disk. The words set, unset
and reload are always read as subcommands, so a configuration section with one
of those names — CouchDB has none — could not be shown.`

// ConfigCmd returns the config command. The Go identifier is not Config
// because this package imports internal/config under that name.
func ConfigCmd() Command {
	return Command{
		Name:    "config",
		Summary: "Show and change the server configuration",
		Example: `$ cdb config log
 SECTION | KEY   | VALUE
---------+-------+-------
 log     | level | info

$ cdb config chttpd_auth/secret
$ cdb config set log/level debug
Set log/level (was "info").

$ cdb config unset log/level --yes
$ cdb config reload`,
		Usage:       "[<section>[/<key>] | set <section>/<key> <value> | unset <section>/<key> | reload]",
		Details:     configDetails,
		MinArgs:     0,
		MaxArgs:     3,
		NeedsClient: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("node", defaultNode, "the node to read or change")
			fs.Bool("reveal", false, "print secret values instead of ****")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			node := inv.String("node")
			if node == "" {
				node = defaultNode
			}
			switch inv.Arg(0) {
			case "set":
				return configSet(ctx, s, inv, node)
			case "unset":
				return configUnset(ctx, s, inv, node)
			case "reload":
				if inv.Arg(1) != "" {
					return nil, Usagef("config", "usage: config reload [--node <name>]")
				}
				if err := s.Client.ReloadConfig(ctx, node); err != nil {
					return nil, err
				}
				return Message{Text: fmt.Sprintf("Reloaded the configuration of %s from its ini files.", node)}, nil
			default:
				return configShow(ctx, s, inv, node)
			}
		},
	}
}

// splitConfigKey splits "section/key" into its two halves. A bare section is
// allowed on show and refused by set and unset, which say so themselves.
func splitConfigKey(arg string) (section, key string) {
	section, key, _ = strings.Cut(arg, "/")
	return section, key
}

// unknownConfigValue is the reason CouchDB gives when a configuration key has
// never been set. The status alone is not enough to go on: a 404 from _config
// is also what a node name nobody recognises produces, and the two want
// different sentences.
const unknownConfigValue = "unknown_config_value"

// configShow prints the whole configuration, one section, or one key.
func configShow(ctx context.Context, s *session.Session, inv Invocation, node string) (Result, error) {
	if inv.Arg(1) != "" {
		return nil, Usagef("config", "usage: config [<section>[/<key>]]")
	}
	section, key := splitConfigKey(inv.Arg(0))
	entries, err := s.Client.Config(ctx, node, section, key)
	if err != nil {
		if ce, ok := couch.AsError(err); ok && key != "" && ce.Reason == unknownConfigValue {
			return nil, Errorf(err, "%s/%s is not set on %s.", section, key, node)
		}
		return nil, err
	}
	reveal := inv.Bool("reveal")
	rows := Rows{Columns: []Column{{Title: "section"}, {Title: "key"}, {Title: "value"}}}
	for _, e := range entries {
		value := redactedConfigValue(e.Section, e.Key, e.Value, reveal)
		rows.Items = append(rows.Items, Row{
			Cells: []string{e.Section, e.Key, value},
			JSON:  jsonObject("section", e.Section, "key", e.Key, "value", value),
		})
	}
	if len(rows.Items) == 0 {
		// A section CouchDB has nothing for answers {} rather than 404, so
		// there is no error to report and no row to draw. An empty table would
		// leave the operator wondering which of the two happened.
		what := "The configuration of " + node
		if section != "" {
			what = "Section [" + section + "] of " + node
		}
		return Message{Text: what + " is empty."}, nil
	}
	return rows, nil
}

// startupNotice is the second sentence "config set" adds for a setting the
// running server will not re-read.
const startupNotice = " This setting is read at start-up; restart CouchDB for it to take effect."

// configSet writes one key. A setting the running server will not re-read is
// confirmed first — it is an operator asking for one thing and getting another
// — while an ordinary key is written as asked.
func configSet(ctx context.Context, s *session.Session, inv Invocation, node string) (Result, error) {
	section, key := splitConfigKey(inv.Arg(1))
	// len, not Arg(2) == "", so "config set s/k \"\"" writes the empty string
	// instead of being mistaken for "config set s/k" with no value at all.
	if section == "" || key == "" || len(inv.Args) < 3 {
		return nil, Usagef("config", "usage: config set <section>/<key> <value>")
	}
	value := inv.Arg(2)
	startup := startupOnly(section, key)
	if startup {
		if err := Confirm(ctx, s, fmt.Sprintf("Change %s/%s on %s?", section, key, node)); err != nil {
			return nil, err
		}
	}
	old, err := s.Client.SetConfig(ctx, node, section, key, value)
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("Set %s/%s %s.", section, key, wasClause(section, key, old, inv.Bool("reveal")))
	if startup {
		text += startupNotice
	}
	return Message{Text: text}, nil
}

// configUnset removes one key. It always confirms: unlike a set, there is no
// value to put back.
func configUnset(ctx context.Context, s *session.Session, inv Invocation, node string) (Result, error) {
	section, key := splitConfigKey(inv.Arg(1))
	if section == "" || key == "" || inv.Arg(2) != "" {
		return nil, Usagef("config", "usage: config unset <section>/<key>")
	}
	if err := Confirm(ctx, s, fmt.Sprintf("Remove %s/%s from %s?", section, key, node)); err != nil {
		return nil, err
	}
	old, err := s.Client.DeleteConfig(ctx, node, section, key)
	if err != nil {
		if ce, ok := couch.AsError(err); ok && ce.Reason == unknownConfigValue {
			return nil, Errorf(err, "%s/%s is not set on %s.", section, key, node)
		}
		return nil, err
	}
	text := fmt.Sprintf("Removed %s/%s %s.", section, key, wasClause(section, key, old, inv.Bool("reveal")))
	if startupOnly(section, key) {
		text += startupNotice
	}
	return Message{Text: text}, nil
}

// wasClause is the parenthesis naming the value that was replaced or removed,
// redacted by the same rule the table uses. A key that had no value gets a
// clause of its own rather than (was "").
func wasClause(section, key, old string, reveal bool) string {
	if old == "" {
		return "(it had no value)"
	}
	return fmt.Sprintf("(was %q)", redactedConfigValue(section, key, old, reveal))
}
