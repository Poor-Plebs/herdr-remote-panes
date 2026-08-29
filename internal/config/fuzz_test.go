package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzASettingsFileIsSurvivable throws whatever at the settings file.
//
// It is the one file a person edits by hand, and the daemon rereads it every
// pass so an edit takes effect without a restart. So it is read constantly,
// written by somebody who may have got it wrong, and what comes out of it
// decides how often machines are polled and how many panes each may open.
//
// Contracts rather than outputs: reading is either an error or a config the
// daemon can use without falling over, the numbers that come out are ones a
// caller can act on, and reading twice gives the same answer.
func FuzzASettingsFileIsSurvivable(f *testing.F) {
	for _, seed := range []string{
		`{}`, ``, `null`, `[]`, `0`, `"x"`, `{`,
		`{"hosts":[]}`,
		`{"hosts":[{"target":"bot"}]}`,
		`{"hosts":[{"target":"bot"},{"target":"bot"}]}`,
		`{"hosts":null}`,
		`{"poll_interval":"2s"}`,
		`{"poll_interval":"0s"}`,
		`{"poll_interval":"-5h"}`,
		`{"poll_interval":"nonsense"}`,
		`{"poll_interval":30}`,
		`{"max_mirrors":0}`,
		`{"max_mirrors":-1}`,
		`{"max_mirrors":999999999}`,
		`{"mode":"attach"}`,
		`{"mode":"nonsense"}`,
		`{"placement":"follow"}`,
		`{"scope":"all","session":""}`,
		`{"label_format":"{name}@{host}"}`,
		`{"label_format":"{"}`,
		`{"hosts":[{"target":"bot","mode":"observe","max_mirrors":-3}]}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}

		cfg, err := Load()
		if err != nil {
			// A settings file that cannot be read is reported, and the daemon
			// says so rather than running on it. Nothing more to hold.
			return
		}

		// The gap between passes is a timer's period: zero spins the daemon as
		// fast as the machine allows, and negative is a timer that panics.
		if got := cfg.Interval(); got <= 0 {
			t.Fatalf("Load() accepted a poll interval of %v from %q", got, raw)
		}
		// The cap decides how many panes a machine may open here. Zero or less
		// is either no mirroring at all or an unbounded number, and both are
		// somebody's screen.
		if cfg.MaxMirrors <= 0 {
			t.Fatalf("Load() accepted a mirror cap of %d from %q", cfg.MaxMirrors, raw)
		}

		// Every accessor a pass calls, on every machine it was given. These
		// run under the daemon's lock, so one that panics takes the daemon.
		for _, host := range cfg.Hosts {
			_ = cfg.SessionFor(host)
			_ = cfg.BinFor(host)
			_ = cfg.ModeFor(host)
			_ = cfg.PlacementFor(host)
			_ = cfg.WorkspaceLabelFor(host, true)
		}
		_ = cfg.Problems()

		// Read twice: the daemon rereads this every pass, and a reader that
		// drifts makes the settings depend on how often they were looked at.
		again, errAgain := Load()
		if (errAgain != nil) != (err != nil) || again.Interval() != cfg.Interval() ||
			again.MaxMirrors != cfg.MaxMirrors || len(again.Hosts) != len(cfg.Hosts) {
			t.Fatalf("Load() of %q gave two different answers", raw)
		}
		_ = time.Second
	})
}
