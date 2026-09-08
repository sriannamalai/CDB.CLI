package shell

import (
	"encoding/json"
	"testing"
)

func TestApplyFilterSelectsAField(t *testing.T) {
	in := []json.RawMessage{json.RawMessage(`{"name":"alice","n":1}`), json.RawMessage(`{"name":"bob","n":2}`)}
	out, err := ApplyFilter(".name", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || string(out[0]) != `"alice"` || string(out[1]) != `"bob"` {
		t.Errorf("out = %v", out)
	}
}

func TestApplyFilterCanExpandAndDrop(t *testing.T) {
	in := []json.RawMessage{json.RawMessage(`{"tags":["a","b"]}`)}
	out, err := ApplyFilter(".tags[]", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("out = %v, want two values", out)
	}
	out, err = ApplyFilter("select(.missing)", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("out = %v, want nothing", out)
	}
}

func TestApplyFilterReportsASyntaxError(t *testing.T) {
	if _, err := ApplyFilter(".[", nil); err == nil {
		t.Fatal("ApplyFilter accepted an invalid expression")
	}
}

func TestApplyFilterReportsARuntimeError(t *testing.T) {
	in := []json.RawMessage{json.RawMessage(`1`)}
	if _, err := ApplyFilter(".name", in); err == nil {
		t.Fatal("ApplyFilter hid a runtime error")
	}
}
