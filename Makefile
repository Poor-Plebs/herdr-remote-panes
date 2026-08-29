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

.PHONY: check fmt vet lint test build mutants vuln clean

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

## build: the binary Herdr runs
build:
	sh build.sh

clean:
	rm -rf bin
