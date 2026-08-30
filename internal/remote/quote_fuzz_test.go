package remote

import (
	"os/exec"
	"strings"
	"testing"
)

// FuzzShellQuoteRoundTrips holds the one place this plugin writes a shell
// command for another machine to the shell that will read it.
//
// Everything in a remote command line goes through shellQuote: the session name
// and the path to Herdr, which come from a config file, and the arguments,
// which include terminal and pane ids that came back from the machine itself.
// So this is both a boundary against odd characters somebody typed and a
// boundary against whatever the far side called its terminals.
//
// The property is round-tripping rather than any particular quoting: hand the
// result to a shell and the argument it gets must be the string that went in.
// A test beside this one holds a list of the cases worth naming; this looks for
// the ones nobody thought of.
//
// Slow by nature -- one process per input, so thousands rather than millions of
// executions in a run. Worth it here because the shell is the thing being
// asked, and a reimplementation of its quoting rules would only be this
// function again with the same blind spots.
func FuzzShellQuoteRoundTrips(f *testing.F) {
	for _, seed := range []string{
		"", "bot", "-", "--flag", "a b", "'", `'\''`, "$", "$(id)", "`id`",
		"a$'\\n'b", "!history", "line\nbreak", "tab\there", "héllo", "🌩",
		"\x00", "\\", "|;&<>()", "~root", "*?[]", "a\rb",
		// A metacharacter with a name after it, which is the shape the ones
		// above miss: "$" alone is literal to a shell and round-trips even
		// unquoted, so a safe-list that wrongly admitted "$" passed every seed
		// here. Fuzzing found "$0" in under two seconds; these are so the
		// ordinary run finds it too.
		"$0", "$HOME", "${HOME}", "a\\b", "\\$", "~", "~/x",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		// A NUL cannot survive an argv at all, so no quoting could make this
		// true and the shell is not what is wrong.
		if strings.ContainsRune(in, 0) {
			t.Skip("no argument can carry a NUL")
		}
		// printf %s prints its argument with no interpretation of its own, so
		// whatever comes back is exactly what the shell handed it.
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(in)).Output()
		if err != nil {
			t.Fatalf("shellQuote(%q) = %q, which the shell rejected: %v", in, shellQuote(in), err)
		}
		if string(out) != in {
			t.Fatalf("shellQuote(%q) = %q, and the shell read it as %q",
				in, shellQuote(in), string(out))
		}
	})
}
