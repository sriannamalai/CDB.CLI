package session

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
)

// fieldSampleSize is how many documents are sampled to learn field names.
const fieldSampleSize = 50

// Cache holds per-session lookups that back completion. It is safe for
// concurrent use.
type Cache struct {
	mu        sync.Mutex
	databases []string
	dbsLoaded bool
	fields    map[string][]string
}

// Cache returns the session's completion cache, creating it on first use.
func (s *Session) Cache() *Cache {
	if s.cache == nil {
		s.cache = &Cache{fields: map[string][]string{}}
	}
	return s.cache
}

// Databases returns the database list, fetching it at most once per session
// until it is invalidated.
func (c *Cache) Databases(ctx context.Context, cl *couch.Client) ([]string, error) {
	c.mu.Lock()
	if c.dbsLoaded {
		out := append([]string(nil), c.databases...)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	names, err := cl.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	c.mu.Lock()
	c.databases = names
	c.dbsLoaded = true
	out := append([]string(nil), names...)
	c.mu.Unlock()
	return out, nil
}

// InvalidateDatabases forces the next Databases call to refetch. mkdir and
// rmdir call it.
func (c *Cache) InvalidateDatabases() {
	c.mu.Lock()
	c.dbsLoaded = false
	c.databases = nil
	c.mu.Unlock()
}

// Fields returns the sorted union of top-level field names across a sample of
// documents in db, cached per database.
func (c *Cache) Fields(ctx context.Context, cl *couch.Client, db string) ([]string, error) {
	c.mu.Lock()
	if f, ok := c.fields[db]; ok {
		out := append([]string(nil), f...)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	// "_id greater than null" is the Mango idiom for "every document"; it uses
	// the built-in _all_docs index, so the sample needs no index of its own.
	page, err := cl.Find(ctx, db, couch.FindOptions{
		Selector: json.RawMessage(`{"_id":{"$gt":null}}`),
		Limit:    fieldSampleSize,
	})
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	for _, doc := range page.Docs {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(doc, &m); err != nil {
			continue
		}
		for k := range m {
			set[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)

	c.mu.Lock()
	if c.fields == nil {
		c.fields = map[string][]string{}
	}
	c.fields[db] = out
	c.mu.Unlock()
	return append([]string(nil), out...), nil
}

// InvalidateFields drops the cached field names for one database.
func (c *Cache) InvalidateFields(db string) {
	c.mu.Lock()
	delete(c.fields, db)
	c.mu.Unlock()
}

// Reset empties the whole cache.
func (c *Cache) Reset() {
	c.mu.Lock()
	c.databases = nil
	c.dbsLoaded = false
	c.fields = map[string][]string{}
	c.mu.Unlock()
}

// setDatabasesForTest seeds the cache. It exists for the session tests.
func (c *Cache) setDatabasesForTest(names []string) {
	c.mu.Lock()
	c.databases = names
	c.dbsLoaded = true
	c.mu.Unlock()
}
