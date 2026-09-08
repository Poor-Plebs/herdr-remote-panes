package syncd

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/project"
)

// sendingCommand matches a Command literal that names the word it is sending,
// which is how every caller in this tree builds one.
var sendingCommand = regexp.MustCompile(`Command\{\s*Cmd:\s*"([a-z-]+)"`)

// commandWordsSent is every command word this plugin sends the daemon, read
// from the call sites rather than from a list written beside them.
//
// Taken from the whole tree because the senders are spread across packages that
// have nothing else in common: the menu asks in internal/picker, the status and
// version commands in internal/cli, and each does it for its own reason. A list
// here would be a second copy of a fact, and the copy nobody updates.
func commandWordsSent(t *testing.T) []string {
	t.Helper()

	root, err := project.Root()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	files := 0
	walked := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// The root's own name is handed back by WalkDir like any other, so
			// a dot-directory skip prunes everything before it starts unless
			// the root is excepted.
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		for _, found := range sendingCommand.FindAllSubmatch(raw, -1) {
			seen[string(found[1])] = true
		}
		return nil
	})
	if walked != nil {
		t.Fatal(walked)
	}

	// Denominators, because a walk that reads nothing and a tree that sends
	// nothing look the same from here.
	if files < 10 {
		t.Fatalf("the walk read %d source files, which is too few to have "+
			"covered this repository", files)
	}
	words := make([]string, 0, len(seen))
	for word := range seen {
		words = append(words, word)
	}
	return words
}

func TestEveryCommandThePluginSendsIsOneTheDaemonHasACaseFor(t *testing.T) {
	// Both halves of this are written down and were compared by nothing. The
	// word is typed at the sender and again in dispatch's switch, and every
	// test that reaches a sender goes through a daemon double that answers any
	// command with the same reply -- so `Cmd: "status"` could be misspelled in
	// internal/picker's statusFor, internal/cli's reportStatus and its
	// reportVersion, all three, with the whole gate green. Measured before this
	// was written: each of those three survived being changed to nonsense.
	//
	// It is silent when it happens. Ask does not read reply.OK, so the caller
	// gets a reply with no error and no machines in it: the menu draws an empty
	// list, which is what a daemon with nothing connected looks like.
	words := commandWordsSent(t)
	if len(words) < 5 {
		t.Fatalf("only %d command words were found in the tree (%v), which is "+
			"fewer than this plugin sends", len(words), words)
	}
	// The one this test was written for, named so a scan that quietly stopped
	// matching cannot pass by finding a shorter list.
	if !slices.Contains(words, "status") {
		t.Fatalf("the scan did not find the status command anywhere: %v", words)
	}

	withFakeHerdr(t)
	d := New(machineConfig())

	for _, word := range words {
		if reply := d.dispatch(Command{Cmd: word}); reply.Message == unknownCommand(word) {
			t.Errorf("this plugin sends %q and the daemon has no case for it, so "+
				"it answers %q", word, reply.Message)
		}
	}

	// The control, and it carries more than it looks. The loop above decides by
	// RECOGNISING the default arm's answer, so anything that changes that
	// sentence -- rewording it, or removing the arm -- makes the loop match
	// nothing and wave every word through. Both go through unknownCommand, so
	// this assertion is what fails when the wording moves, loudly, instead of
	// the check above going quietly blind. Measured: rewording the default arm
	// is caught here and nowhere else.
	if reply := d.dispatch(Command{Cmd: "not-a-command"}); reply.Message != unknownCommand("not-a-command") {
		t.Errorf("a word the daemon has no case for came back as %q, so the "+
			"check above is reading nothing", reply.Message)
	}
}

// unknownCommand is what dispatch answers a word it has no case for.
func unknownCommand(word string) string {
	return "unknown command " + word
}
