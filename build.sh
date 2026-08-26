#!/bin/sh
# Builds the plugin, and says why it cannot when it cannot.
#
# Herdr runs this instead of `go build` directly so that a machine without Go
# gets an answer rather than "failed to start: No such file or directory",
# which is the operating system reporting that `go` could not be executed and
# reads like the plugin is broken. Anything named here has to exist for that
# message to be printable at all, so it is `sh` and nothing else.
set -eu

go_bin=""
if command -v go >/dev/null 2>&1; then
	go_bin=go
else
	# The same trouble this plugin already solves on the far side of an ssh
	# connection: a session started by a login manager or a service unit
	# inherits a much shorter PATH than a login shell, and Go is usually
	# installed into exactly the part that goes missing.
	for candidate in \
		/usr/local/go/bin/go \
		"${HOME:-}/go/bin/go" \
		"${HOME:-}/.local/bin/go" \
		/opt/homebrew/bin/go \
		/usr/lib/go/bin/go \
		"${HOME:-}/.nix-profile/bin/go" \
		"${HOME:-}/.local/share/mise/shims/go"; do
		if [ -x "$candidate" ]; then
			go_bin=$candidate
			break
		fi
	done
fi

if [ -z "$go_bin" ]; then
	cat >&2 <<'MSG'
herdr-remote-panes is built from source, and Go was not found on this machine.

Install Go 1.25 or newer (https://go.dev/dl/) and install the plugin again.

If Go is already installed, Herdr cannot see it: a session started by a login
manager or a service unit has a shorter PATH than your shell. Check with

    tr '\0' '\n' < /proc/"$(pgrep -n herdr)"/environ | grep '^PATH='

and put Go's directory somewhere that session reads -- ~/.profile rather than
~/.bashrc, for most desktop sessions.
MSG
	exit 1
fi

exec "$go_bin" build -trimpath -o bin/herdr-remote-panes .
