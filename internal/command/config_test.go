package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestConfigShowsASectionSorted(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/log", 200, `{"writer":"stderr","level":"info"}`)
	s := connected(t, srv)
	res, err := invoke(t, ConfigCmd(), s, "log")
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	titles := []string{"section", "key", "value"}
	for i, want := range titles {
		if rows.Columns[i].Title != want {
			t.Fatalf("column %d = %q", i, rows.Columns[i].Title)
		}
	}
	if len(rows.Items) != 2 {
		t.Fatalf("got %d rows", len(rows.Items))
	}
	if got := rows.Items[0].Cells; got[0] != "log" || got[1] != "level" || got[2] != "info" {
		t.Errorf("row 0 = %v", got)
	}
	if rows.Items[1].Cells[1] != "writer" {
		t.Errorf("rows are not sorted by key: %v", rows.Items[1].Cells)
	}
}

func TestConfigRedactsSecretsEverywhereButUnderReveal(t *testing.T) {
	const body = `{"secret":"proxysecret","require_valid_user":"false"}`
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/chttpd_auth", 200, body)
	s := connected(t, srv)
	res, err := invoke(t, ConfigCmd(), s, "chttpd_auth")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	for _, item := range rows.Items {
		if item.Cells[1] != "secret" {
			continue
		}
		if item.Cells[2] != "****" {
			t.Errorf("secret cell = %q", item.Cells[2])
		}
		var payload map[string]string
		if err := json.Unmarshal(item.JSON, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["value"] != "****" {
			t.Errorf("--json leaked the secret: %s", item.JSON)
		}
	}

	srv2 := couchtest.New(t)
	srv2.JSON("GET", "/_node/_local/_config/chttpd_auth", 200, body)
	s2 := connected(t, srv2)
	res2, err := invoke(t, ConfigCmd(), s2, "chttpd_auth", "--reveal")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range res2.(Rows).Items {
		if item.Cells[1] == "secret" && item.Cells[2] == "proxysecret" {
			found = true
		}
	}
	if !found {
		t.Error("--reveal did not show the secret")
	}
}

func TestConfigRedactionList(t *testing.T) {
	for _, tc := range []struct{ section, key string }{
		{"admins", "alice"},
		{"jwt_keys", "hmac:_default"},
		{"chttpd_auth", "secret"},
		{"couch_httpd_auth", "secret"},
		{"replicator", "worker_password"},
		{"somewhere", "api_token"},
		{"somewhere", "shared_secret"},
	} {
		if got := redactedConfigValue(tc.section, tc.key, "hunter2", false); got != "****" {
			t.Errorf("%s/%s rendered as %q", tc.section, tc.key, got)
		}
		if got := redactedConfigValue(tc.section, tc.key, "hunter2", true); got != "hunter2" {
			t.Errorf("%s/%s under --reveal rendered as %q", tc.section, tc.key, got)
		}
	}
	if got := redactedConfigValue("log", "level", "info", false); got != "info" {
		t.Errorf("an ordinary value was redacted: %q", got)
	}
}

// A key that has never been set answers 404 with the reason
// "unknown_config_value". The generic 404 sentence would say the configuration
// of the node was not found, which is both wrong and alarming.
func TestConfigOnAKeyThatIsNotSetSaysSo(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/log/level", 404, `{"error":"not_found","reason":"unknown_config_value"}`)
	s := connected(t, srv)
	_, err := invoke(t, ConfigCmd(), s, "log/level")
	if err == nil {
		t.Fatal("a key that is not set was reported as a success")
	}
	if err.Error() != "log/level is not set on _local." {
		t.Errorf("error = %q", err)
	}
	var ue *UsageError
	if errors.As(err, &ue) {
		t.Error("a key that is not set was reported as a usage error")
	}
}

// A section CouchDB knows nothing about answers {}, which is no rows at all.
// An empty table says less than a sentence does.
func TestConfigOnAnEmptySectionSaysSoInWords(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/nouveau", 200, `{}`)
	s := connected(t, srv)
	res, err := invoke(t, ConfigCmd(), s, "nouveau")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok {
		t.Fatalf("result is %T, want Message", res)
	}
	if msg.Text != "Section [nouveau] of _local is empty." {
		t.Errorf("message = %q", msg.Text)
	}
}

func TestConfigSetReportsTheOldValueAndDoesNotAsk(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/_node/_local/_config/log/level", 200, `"info"`)
	s := connected(t, srv)
	res, err := invoke(t, ConfigCmd(), s, "set", "log/level", "debug")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || msg.Text != `Set log/level (was "info").` {
		t.Fatalf("message = %#v", res)
	}
	if body := strings.TrimSpace(string(srv.Last("PUT", "/_node/_local/_config/log/level").Body)); body != `"debug"` {
		t.Errorf("PUT body = %s", body)
	}
	if out := s.Stdout.(*bytes.Buffer).String(); out != "" {
		t.Errorf("an ordinary key was confirmed: %q", out)
	}
}

func TestConfigSetOnAnUnsetKeySaysSo(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/_node/_local/_config/log/level", 200, `""`)
	s := connected(t, srv)
	res, err := invoke(t, ConfigCmd(), s, "set", "log/level", "debug")
	if err != nil {
		t.Fatal(err)
	}
	if msg := res.(Message).Text; msg != "Set log/level (it had no value)." {
		t.Errorf("message = %q", msg)
	}
}

func TestConfigSetRedactsTheOldSecret(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/_node/_local/_config/chttpd_auth/secret", 200, `"oldsecret"`)
	s := connected(t, srv)
	res, err := invoke(t, ConfigCmd(), s, "set", "chttpd_auth/secret", "newsecret")
	if err != nil {
		t.Fatal(err)
	}
	msg := res.(Message).Text
	if strings.Contains(msg, "oldsecret") || strings.Contains(msg, "newsecret") {
		t.Fatalf("the message leaked a secret: %q", msg)
	}
	if msg != `Set chttpd_auth/secret (was "****").` {
		t.Errorf("message = %q", msg)
	}
}

func TestConfigSetOnAStartupOnlyKeyAsksAndWarns(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/_node/_local/_config/chttpd/port", 200, `"5984"`)
	s := connected(t, srv)
	s.SetStdin(strings.NewReader("y\n"))
	s.Prefs.Interactive = true
	res, err := invoke(t, ConfigCmd(), s, "set", "chttpd/port", "5985")
	if err != nil {
		t.Fatal(err)
	}
	out := s.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "Change chttpd/port on _local?") {
		t.Errorf("prompt = %q", out)
	}
	want := `Set chttpd/port (was "5984"). This setting is read at start-up; restart CouchDB for it to take effect.`
	if msg := res.(Message).Text; msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
}

func TestConfigSetOnAStartupOnlyKeyStopsWhenDeclined(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetStdin(strings.NewReader("n\n"))
	s.Prefs.Interactive = true
	_, err := invoke(t, ConfigCmd(), s, "set", "chttpd/port", "5985")
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("error = %v", err)
	}
	if srv.Last("PUT", "/_node/_local/_config/chttpd/port") != nil {
		t.Error("a declined change was written anyway")
	}
}

func TestConfigUnsetAlwaysConfirms(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("DELETE", "/_node/_local/_config/log/level", 200, `"debug"`)
	s := connected(t, srv)
	s.SetStdin(strings.NewReader("y\n"))
	s.Prefs.Interactive = true
	res, err := invoke(t, ConfigCmd(), s, "unset", "log/level")
	if err != nil {
		t.Fatal(err)
	}
	if out := s.Stdout.(*bytes.Buffer).String(); !strings.Contains(out, "Remove log/level from _local?") {
		t.Errorf("prompt = %q", out)
	}
	if msg := res.(Message).Text; msg != `Removed log/level (was "debug").` {
		t.Errorf("message = %q", msg)
	}
}

// Removing a key that was never set answers the same 404 the read does, and
// wants the same sentence rather than "Configuration of _local was not found."
func TestConfigUnsetOnAKeyThatIsNotSetSaysSo(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("DELETE", "/_node/_local/_config/log/level", 404, `{"error":"not_found","reason":"unknown_config_value"}`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	_, err := invoke(t, ConfigCmd(), s, "unset", "log/level")
	if err == nil {
		t.Fatal("removing a key that is not set was reported as a success")
	}
	if err.Error() != "log/level is not set on _local." {
		t.Errorf("error = %q", err)
	}
}

func TestConfigReloadSaysWhatItDid(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_node/_local/_config/_reload", 200, `{"ok":true}`)
	s := connected(t, srv)
	res, err := invoke(t, ConfigCmd(), s, "reload")
	if err != nil {
		t.Fatal(err)
	}
	if msg := res.(Message).Text; msg != "Reloaded the configuration of _local from its ini files." {
		t.Errorf("message = %q", msg)
	}
}

func TestConfigNodeFlagReachesTheURL(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/couchdb@127.0.0.1/_config/log", 200, `{"level":"info"}`)
	s := connected(t, srv)
	if _, err := invoke(t, ConfigCmd(), s, "log", "--node", "couchdb@127.0.0.1"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigSetAndUnsetNeedASectionAndKey(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	for _, argv := range [][]string{
		{"set", "log", "debug"},
		{"set", "log/level"},
		{"unset", "log"},
	} {
		_, err := invoke(t, ConfigCmd(), s, argv...)
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Errorf("%v: error = %v, want a usage error", argv, err)
		}
	}
}
