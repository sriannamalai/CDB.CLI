package session

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Value is one variable's value, held as the JSON the shell stored for it so
// that a number stays a number and an object stays an object all the way into
// a jq stage.
type Value struct {
	JSON json.RawMessage
}

// Text is what a "$name" reference expands to inside a word: a JSON string
// expands to the string itself — "$rev" is a revision, not a quoted revision —
// and everything else to its compact JSON.
func (v Value) Text() string {
	// Decoding into any rather than into a string is what tells a JSON string
	// apart from JSON null: unmarshalling null into a string succeeds and
	// leaves it empty, which would print the null as nothing at all.
	var out any
	if err := json.Unmarshal(v.JSON, &out); err == nil {
		if s, ok := out.(string); ok {
			return s
		}
	}
	return string(v.JSON)
}

// Any is the value as jq sees it.
func (v Value) Any() any {
	var out any
	if err := json.Unmarshal(v.JSON, &out); err != nil {
		return nil
	}
	return out
}

// StringValue builds a Value from plain text, which is what "set name words…"
// stores.
func StringValue(s string) Value {
	b, err := json.Marshal(s)
	if err != nil {
		return Value{JSON: json.RawMessage(`""`)}
	}
	return Value{JSON: b}
}

// Binding is one row of a variable listing.
type Binding struct {
	Name  string
	Value Value
}

// Vars is a table of shell variables. A child table reads through to its
// parent and writes locally, so a script can never change its caller's
// variables. Nothing here is persisted.
type Vars struct {
	parent *Vars
	values map[string]Value
}

// NewVars returns an empty table with no parent.
func NewVars() *Vars { return &Vars{values: map[string]Value{}} }

// Child returns a table that reads through to v.
func (v *Vars) Child() *Vars { return &Vars{parent: v, values: map[string]Value{}} }

// Lookup finds the nearest definition of a name.
func (v *Vars) Lookup(name string) (Value, bool) {
	for t := v; t != nil; t = t.parent {
		if val, ok := t.values[name]; ok {
			return val, true
		}
	}
	return Value{}, false
}

// Set defines a name in this table.
func (v *Vars) Set(name string, val Value) { v.values[name] = val }

// Unset removes a name from this table. It does not reach into the parent: a
// script cannot unset its caller's variable any more than it can set one.
func (v *Vars) Unset(name string) { delete(v.values, name) }

// List returns every visible binding, sorted by name, the nearest definition
// of each winning.
func (v *Vars) List() []Binding {
	seen := map[string]Value{}
	for t := v; t != nil; t = t.parent {
		for name, val := range t.values {
			if _, ok := seen[name]; !ok {
				seen[name] = val
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Binding, 0, len(names))
	for _, name := range names {
		out = append(out, Binding{Name: name, Value: seen[name]})
	}
	return out
}

// All is every visible binding as jq sees it, for compiling a filter stage.
// "env" is left out: "$env" is jq's word, not a shell variable's.
func (v *Vars) All() map[string]any {
	out := map[string]any{}
	for _, b := range v.List() {
		if b.Name == "env" {
			continue
		}
		out[b.Name] = b.Value.Any()
	}
	return out
}

// namePattern is what a variable name has to look like.
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName reports whether name may be defined.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Masked reports whether a variable's value must be printed as "****". A name
// ending in "password", "secret" or "token" holds a credential, and the rule
// that a secret never reaches stdout, a log or the history file does not stop
// at the ones cdb itself put there.
func Masked(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, "password") || strings.HasSuffix(l, "secret") || strings.HasSuffix(l, "token")
}
