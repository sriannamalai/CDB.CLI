package session

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestCacheDatabasesIsFetchedOnce(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_all_dbs", 200, `["mydb","other"]`)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	s := New(bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{})
	s.Attach(c, "test")
	defer s.Detach()

	for i := 0; i < 3; i++ {
		names, err := s.Cache().Databases(context.Background(), s.Client)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 2 {
			t.Fatalf("names = %v", names)
		}
	}
	calls := 0
	for _, r := range srv.Requests() {
		if r.Path == "/_all_dbs" {
			calls++
		}
	}
	if calls != 1 {
		t.Errorf("_all_dbs was fetched %d times, want 1", calls)
	}
	s.Cache().InvalidateDatabases()
	if _, err := s.Cache().Databases(context.Background(), s.Client); err != nil {
		t.Fatal(err)
	}
	calls = 0
	for _, r := range srv.Requests() {
		if r.Path == "/_all_dbs" {
			calls++
		}
	}
	if calls != 2 {
		t.Errorf("after invalidation _all_dbs was fetched %d times, want 2", calls)
	}
}

func TestCacheFieldsSamplesDocuments(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[
		{"_id":"a","name":"alice","age":30},
		{"_id":"b","name":"bob","email":"b@example.com"}]}`)
	c, _ := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	s := New(bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{})
	s.Attach(c, "test")
	defer s.Detach()

	fields, err := s.Cache().Fields(context.Background(), s.Client, "mydb")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"_id": true, "name": true, "age": true, "email": true}
	if len(fields) != len(want) {
		t.Fatalf("fields = %v, want %d entries", fields, len(want))
	}
	for _, f := range fields {
		if !want[f] {
			t.Errorf("unexpected field %q", f)
		}
	}
	for i := 1; i < len(fields); i++ {
		if fields[i-1] > fields[i] {
			t.Fatalf("fields are not sorted: %v", fields)
		}
	}
	if _, err := s.Cache().Fields(context.Background(), s.Client, "mydb"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, r := range srv.Requests() {
		if r.Path == "/mydb/_find" {
			calls++
		}
	}
	if calls != 1 {
		t.Errorf("_find was called %d times, want 1", calls)
	}
	if got := srv.Last("POST", "/mydb/_find").Body; !bytes.Contains(got, []byte(`"limit":50`)) {
		t.Errorf("field sampling body = %s, want limit 50", got)
	}
	if got := srv.Last("POST", "/mydb/_find").Body; bytes.Contains(got, []byte(`"skip"`)) {
		t.Errorf("field sampling body = %s, want no skip", got)
	}
	s.Cache().InvalidateFields("mydb")
	if _, err := s.Cache().Fields(context.Background(), s.Client, "mydb"); err != nil {
		t.Fatal(err)
	}
	calls = 0
	for _, r := range srv.Requests() {
		if r.Path == "/mydb/_find" {
			calls++
		}
	}
	if calls != 2 {
		t.Errorf("after invalidation _find was called %d times, want 2", calls)
	}
}

func TestAttachResetsTheCache(t *testing.T) {
	s := New(bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{})
	s.Cache().setDatabasesForTest([]string{"stale"})
	srv := couchtest.New(t)
	srv.JSON("GET", "/_all_dbs", 200, `["fresh"]`)
	c, _ := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	s.Attach(c, "test")
	defer s.Detach()
	names, err := s.Cache().Databases(context.Background(), s.Client)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "fresh" {
		t.Errorf("names = %v, want [fresh]", names)
	}
}

// The stages of a shell pipeline run concurrently and any of them may be the
// first to ask for the cache, so its creation, its reads and its invalidations
// all have to survive being done at once. Run with -race.
func TestCacheIsSafeForConcurrentUse(t *testing.T) {
	s := New(strings.NewReader(""), io.Discard, io.Discard)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := s.Cache()
			switch i % 4 {
			case 0:
				c.InvalidateDatabases()
			case 1:
				c.InvalidateFields("movies")
			case 2:
				c.Reset()
			default:
				c.setDatabasesForTest([]string{"movies"})
			}
			_ = s.Cache()
		}(i)
	}
	wg.Wait()
	if s.Cache() == nil {
		t.Fatal("the cache was never created")
	}
}
