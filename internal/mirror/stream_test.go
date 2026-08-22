package mirror

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeFrame(t *testing.T) {
	payload := "\x1b[32mhello\x1b[0m"
	good := `{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(payload)) + `"}`

	t.Run("a frame yields its terminal output", func(t *testing.T) {
		raw, ok := decodeFrame([]byte(good))
		if !ok {
			t.Fatal("a valid frame was rejected")
		}
		if string(raw) != payload {
			t.Errorf("got %q, want %q", raw, payload)
		}
	})

	// The stream is a live feed of someone's terminal. One unreadable line is
	// no reason to tear down a working terminal, so each of these is skipped
	// rather than fatal.
	skipped := map[string]string{
		"an empty line":              "",
		"whitespace":                 "   \t ",
		"a line that is not JSON":    "herdr: something happened",
		"malformed JSON":             `{"bytes":`,
		"a frame with no payload":    `{"bytes":""}`,
		"a different message":        `{"type":"resize","cols":80}`,
		"payload that is not base64": `{"bytes":"not!valid!base64!"}`,
		"an empty payload":           `{"bytes":"` + base64.StdEncoding.EncodeToString(nil) + `"}`,
	}
	for name, line := range skipped {
		t.Run(name+" is skipped", func(t *testing.T) {
			if _, ok := decodeFrame([]byte(line)); ok {
				t.Errorf("decodeFrame(%q) should have been skipped", line)
			}
		})
	}

	t.Run("surrounding whitespace does not matter", func(t *testing.T) {
		if _, ok := decodeFrame([]byte("  " + good + "  ")); !ok {
			t.Error("a frame with surrounding whitespace was rejected")
		}
	})

	t.Run("binary output survives intact", func(t *testing.T) {
		// Terminal output is arbitrary bytes, including invalid UTF-8, and
		// must reach the pane exactly as sent.
		binary := []byte{0x00, 0xff, 0x1b, 0x5b, 0xc3, 0x28}
		frame := `{"bytes":"` + base64.StdEncoding.EncodeToString(binary) + `"}`
		raw, ok := decodeFrame([]byte(frame))
		if !ok {
			t.Fatal("a binary frame was rejected")
		}
		if string(raw) != string(binary) {
			t.Errorf("got % x, want % x", raw, binary)
		}
	})
}

func TestParseWindowSize(t *testing.T) {
	// stty size reports "rows cols", in that order — reversing them would open
	// the remote stream at the wrong shape.
	cols, rows := parseWindowSize("24 80\n")
	if rows != 24 || cols != 80 {
		t.Errorf("got %dx%d (cols x rows), want 80x24", cols, rows)
	}

	for name, out := range map[string]string{
		"empty":         "",
		"one number":    "24",
		"three numbers": "24 80 5",
		"not numbers":   "rows cols",
		"zero":          "0 0",
		"negative":      "-1 -1",
	} {
		t.Run(name+" falls back", func(t *testing.T) {
			cols, rows := parseWindowSize(out)
			if cols != defaultCols || rows != defaultRows {
				t.Errorf("parseWindowSize(%q) = %dx%d, want the defaults %dx%d",
					out, cols, rows, defaultCols, defaultRows)
			}
		})
	}

	t.Run("a large terminal is respected", func(t *testing.T) {
		cols, rows := parseWindowSize("120 400")
		if rows != 120 || cols != 400 {
			t.Errorf("got %dx%d, want 400x120", cols, rows)
		}
	})

	t.Run("extra whitespace is fine", func(t *testing.T) {
		if cols, rows := parseWindowSize("  30   100  \n"); rows != 30 || cols != 100 {
			t.Errorf("got %dx%d, want 100x30", cols, rows)
		}
	})
}

func TestDecodeFrameHandlesLongPayloads(t *testing.T) {
	// A screen repaint of a large terminal is a single frame and must not be
	// mistaken for a stream that has gone wrong.
	payload := strings.Repeat("x", 200_000)
	frame := `{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(payload)) + `"}`
	if len(frame) >= maxFrameBytes {
		t.Fatalf("test frame of %d bytes exceeds the limit", len(frame))
	}
	raw, ok := decodeFrame([]byte(frame))
	if !ok || len(raw) != len(payload) {
		t.Errorf("a large repaint should decode intact, got %d bytes ok=%v", len(raw), ok)
	}
}
