# What CI runs, runnable here.
#
# It lived only in the workflow, so the way to know whether a change would pass
# was to push it and wait. That is slow, and it is easy to join a test run and a
# commit with a semicolon and not notice the tests failed.

# Pinned to the same version CI uses: a new release adding a check should not
# fail a change that has nothing to do with it.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.1

.PHONY: check fmt vet lint test build mutants clean

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

## build: the binary Herdr runs
build:
	sh build.sh

clean:
	rm -rf bin
