# What CI runs, runnable here.
#
# It lived only in the workflow, so the way to know whether a change would pass
# was to push it and wait. That is slow, and it is easy to join a test run and a
# commit with a semicolon and not notice the tests failed.

# Pinned to the same version CI uses: a new release adding a check should not
# fail a change that has nothing to do with it.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.1

# Not pinned, unlike staticcheck. What this reports is a database rather than a
# set of checks, and a pinned copy answers about the vulnerabilities that were
# known when it was pinned.
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: check fmt vet lint test build mutants deletions vuln herdr bounds clean

## check: everything CI does, in the order it does it
check: fmt vet lint test build
	@echo "ok — this is what CI runs"

## fmt: fail if anything is not gofmt'd
fmt:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

## vet: the standard checks
vet:
	go vet ./...

## lint: staticcheck, pinned
lint:
	go run $(STATICCHECK) ./...

## test: with the race detector, in a shuffled order
test:
	go test -race -shuffle=on ./...

## mutants: changes to a package that no test would catch
##
## Not part of check: it runs the package's tests once per mutation, so it is
## minutes rather than seconds, and a survivor is something to read rather than
## a failure. Works on a copy under the temp directory; the tree is untouched.
##
##   make mutants PKG=./internal/config
##   make mutants PKG=./internal/syncd FILES=daemon.go
##   make mutants PKG=./internal/syncd SINCE=v0.3.0   # only what changed
mutants:
	@if [ -z "$(PKG)" ]; then echo "usage: make mutants PKG=./internal/config [FILES=one.go]"; exit 2; fi
	SINCE=$(SINCE) go run ./tools/mutants $(PKG) $(FILES)

## deletions: statements that can be removed without any test noticing
##
## `make mutants` flips operators and drops negations, which finds a decision
## nothing checks. It never removes a statement, so a side effect nothing
## checks is invisible to it -- and to everything else here. Thirteen were
## found this way: a command that was never cancelled, a descriptor leaked on
## every rotation, a flag never sent, the same machine listed twice.
##
## Not part of check: it builds and tests the package once per statement, which
## is minutes for a small one and hours for the daemon. A survivor is something
## to read rather than a failure.
##
##   make deletions PKG=./internal/capped
##   make deletions PKG=./internal/syncd SECS=180 ROOT=/tmp/sweep
##
## ROOT sweeps a copy so a long run leaves your tree free to commit from:
##
##   git worktree add --detach /tmp/sweep HEAD
deletions:
	@if [ -z "$(PKG)" ]; then echo "usage: make deletions PKG=./internal/capped [SECS=180] [ROOT=dir]"; exit 2; fi
	go run ./tools/deletions $(if $(ROOT),-root $(ROOT)) $(PKG) $(SECS)

## vuln: known vulnerabilities in what this builds against
##
## Not part of check, because it needs the network and asks a database that
## changes without the code changing: a check that can start failing on a
## machine that has been left alone overnight is one people learn to ignore.
## Worth running before a release, and worth running when nothing has changed.
##
## There are no dependencies -- go.mod has no require block -- so what it
## really asks about is the standard library this was built with.
vuln:
	go run $(GOVULNCHECK) ./...

## bounds: whether each max* in the tree is held by anything
##
## Raises every bound a thousandfold in turn and runs that package's own tests.
## A bound whose loss nothing notices is a bound with no test behind it, and
## the reason to look mechanically is that those do not look like gaps: four
## were found this way, each with a test that measured against the bound it was
## meant to pin, so raising the bound raised what the test expected.
##
## Not part of check: it builds and tests each package once per bound, which is
## minutes. An unheld bound is something to read rather than a failure -- some
## are not observable at all.
##
##   make bounds              # everything under internal/
##   make bounds DIR=./internal/picker
bounds:
	go run ./tools/bounds $(DIR)

## herdr: whether the installed Herdr still takes what this sends it
##
## Not part of check, for the same reason as vuln: it needs Herdr on the
## machine, and CI has no reason to have one. A check that cannot run
## everywhere is one people learn to ignore where it can.
##
## Nothing that builds checks any of this. A renamed flag or a value Herdr
## stopped accepting compiles perfectly and fails at the far end, one action at
## a time -- and the stand-in the tests run against is written from the same
## belief as the code, so it agrees with whatever the code believes. It
## accepted `--placement popup` for as long as the code sent it, while the real
## thing refused it and nothing opened.
##
## Reads internal/herdrcli.Dependencies, which a test holds to the package, and
## asks only for --help: it must not change anything.
herdr:
	go run ./tools/herdrcheck

## build: the binary Herdr runs
build:
	sh build.sh

clean:
	rm -rf bin
