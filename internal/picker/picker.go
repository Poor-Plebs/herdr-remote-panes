// Package picker draws the machine menu shown in a Herdr popup pane.
package picker

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/sshconfig"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
)

// Entry is one machine offered in the menu.
type Entry struct {
	Target string
	// Label is how the machine is named in Herdr, when it is configured.
	Label string
	// Configured marks a machine listed in the plugin config rather than only
	// in the SSH config.
	Configured bool
	Connected  bool
	Mirrors    int
	SSHOnly    bool
}

// Connect asks the daemon to mirror a machine.
type Connect func(target string) (string, error)

// Run draws the menu and connects to whatever the user picks. It returns when
// the user chooses or cancels; the pane closes as soon as it returns.
func Run(connect Connect) error {
	entries := collect()
	if len(entries) == 0 {
		fmt.Print("\r\nNo machines found.\r\n\r\nAdd hosts to ~/.ssh/config or to the plugin's config.json.\r\n")
		waitForKey()
		return nil
	}

	restore := rawMode()
	defer restore()

	selected := 0
	for {
		draw(entries, selected)

		key := readKey()
		switch key {
		case keyUp:
			selected = (selected - 1 + len(entries)) % len(entries)
		case keyDown:
			selected = (selected + 1) % len(entries)
		case keyQuit:
			clear()
			return nil
		case keyEnter:
			return choose(entries[selected], connect)
		default:
			// Digits jump straight to an entry.
			if index := int(key - '1'); index >= 0 && index < len(entries) && index < 9 {
				return choose(entries[index], connect)
			}
		}
	}
}

func choose(entry Entry, connect Connect) error {
	clear()
	fmt.Printf("  Connecting to %s ...\r\n", entry.Target)

	message, err := connect(entry.Target)
	if err != nil {
		fmt.Printf("\r\n  Could not connect: %v\r\n\r\n  Press any key.\r\n", err)
		waitForKey()
		return nil
	}
	fmt.Printf("\r\n  %s\r\n", message)
	return nil
}

// collect merges the machines from the SSH config with those in the plugin
// config, and marks which are already connected.
func collect() []Entry {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Defaults()
	}

	byTarget := map[string]*Entry{}
	var order []string
	add := func(target string) *Entry {
		if existing, ok := byTarget[target]; ok {
			return existing
		}
		entry := &Entry{Target: target}
		byTarget[target] = entry
		order = append(order, target)
		return entry
	}

	for _, host := range cfg.Hosts {
		if host.Disabled {
			continue
		}
		entry := add(host.Target)
		entry.Configured = true
		entry.Label = host.DisplayLabel()
	}
	for _, host := range sshconfig.Hosts() {
		add(host)
	}

	for _, info := range status() {
		if entry, ok := byTarget[info.Target]; ok {
			entry.Connected = info.Connected
			entry.Mirrors = info.Mirrors
			entry.SSHOnly = info.SSHOnly
		}
	}

	entries := make([]Entry, 0, len(order))
	for _, target := range order {
		entries = append(entries, *byTarget[target])
	}
	// Configured machines first; they are the ones being worked on.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Configured && !entries[j].Configured
	})
	return entries
}

// status asks the daemon what it is currently mirroring. A daemon that is not
// running is not an error here: every machine simply shows as unconnected.
func status() []syncd.HostInfo {
	reply, err := syncd.Ask(syncd.Command{Cmd: "status"})
	if err != nil {
		return nil
	}
	return reply.Hosts
}

const (
	esc     = "\x1b"
	reset   = esc + "[0m"
	dim     = esc + "[2m"
	bold    = esc + "[1m"
	green   = esc + "[32m"
	yellow  = esc + "[33m"
	reverse = esc + "[7m"
)

func clear() {
	fmt.Print(esc + "[2J" + esc + "[H")
}

func draw(entries []Entry, selected int) {
	var b strings.Builder
	b.WriteString(esc + "[2J" + esc + "[H")
	b.WriteString("\r\n  " + bold + "Connect to a machine" + reset + "\r\n\r\n")

	for i, entry := range entries {
		marker := "  "
		line := ""
		if i == selected {
			marker = reverse + " >" + reset
		}
		number := "  "
		if i < 9 {
			number = fmt.Sprintf("%d.", i+1)
		}

		name := entry.Target
		if entry.Label != "" && entry.Label != entry.Target {
			name = fmt.Sprintf("%s (%s)", entry.Target, entry.Label)
		}

		switch {
		case entry.Connected && entry.SSHOnly:
			line = green + "connected" + reset + dim + " · ssh panes" + reset
		case entry.Connected:
			line = green + fmt.Sprintf("connected · %d mirrored", entry.Mirrors) + reset
		case entry.Configured:
			line = yellow + "not connected" + reset
		default:
			line = dim + "from ~/.ssh/config" + reset
		}

		b.WriteString(fmt.Sprintf("%s %s %-24s %s\r\n", marker, number, name, line))
	}

	b.WriteString("\r\n  " + dim + "↑↓ or j/k move · 1-9 jump · enter connect · q cancel" + reset + "\r\n")
	fmt.Print(b.String())
}

type key rune

const (
	keyUp    key = 0xE000
	keyDown  key = 0xE001
	keyEnter key = 0xE002
	keyQuit  key = 0xE003
	keyNone  key = 0xE004
)

// readKey reads one keypress, translating the arrow-key escape sequences.
func readKey() key {
	buf := make([]byte, 3)
	n, err := os.Stdin.Read(buf[:1])
	if err != nil || n == 0 {
		return keyQuit
	}

	switch buf[0] {
	case '\r', '\n':
		return keyEnter
	case 'q', 'Q', 3: // 3 is ctrl+c
		return keyQuit
	case 'k':
		return keyUp
	case 'j':
		return keyDown
	case 0x1b:
		// Either a bare Escape or an arrow key.
		if n, _ := os.Stdin.Read(buf[1:3]); n == 2 && buf[1] == '[' {
			switch buf[2] {
			case 'A':
				return keyUp
			case 'B':
				return keyDown
			}
			return keyNone
		}
		return keyQuit
	}
	return key(buf[0])
}

func waitForKey() {
	restore := rawMode()
	defer restore()
	readKey()
}

// rawMode puts the popup's terminal into raw mode so keys arrive unbuffered
// and are not echoed. stty keeps this dependency-free.
func rawMode() func() {
	if err := stty("raw", "-echo"); err != nil {
		return func() {}
	}
	return func() { _ = stty("sane") }
}

func stty(args ...string) error {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
