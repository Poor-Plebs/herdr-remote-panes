package herdrcli

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Every answer from Herdr arrives here as bytes on a pipe, and what is in them
// is not this plugin's to decide: a response, a notice printed beside it, a
// crash, a shell's complaint that the binary was not found. Whatever it is,
// callers go on to unmarshal what comes back, so what comes back has to be
// either a usable result or an error -- never something in between.

func FuzzDecode(f *testing.F) {
	ok := `{"id":"x","result":{"type":"ok"}}`
	for _, seed := range []string{
		"", "   ", "not json", ok, ok + "\n", "\n\n" + ok + "\n\n",
		"note: something\n" + ok, ok + "\nnote: something",
		`{"id":"x","error":{"code":"pane_not_found","message":"gone"}}`,
		`{"id":"x"}`, "{}", `{"result":`, `{"result":{}}` + "\n" + ok,
		"{\n \"id\": \"x\",\n \"result\": 1\n}",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, out []byte) {
		result, err := Decode(out, []string{"pane", "list"})

		if err != nil {
			// A failure hands back nothing to use. Handing back both invites
			// a caller to read the result and never look at the error.
			if result != nil {
				t.Fatalf("Decode(%q) returned an error and %q as well", out, result)
			}
			return
		}
		// No error means the caller will unmarshal this, so it has to be
		// something that can be unmarshalled. A result that is not JSON is a
		// failure reported as a success.
		if result != nil && !json.Valid(result) {
			t.Fatalf("Decode(%q) succeeded with a result that is not JSON: %q", out, result)
		}
		// The same output twice is the same answer: this is called once per
		// command and a decoder that drifts makes a command's outcome depend
		// on how many times it was read.
		again, errAgain := Decode(out, []string{"pane", "list"})
		if (errAgain != nil) != (err != nil) || !bytes.Equal(again, result) {
			t.Fatalf("Decode(%q) gave (%q, %v) then (%q, %v)", out, result, err, again, errAgain)
		}
	})
}
