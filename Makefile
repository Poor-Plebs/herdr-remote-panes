# What CI runs, runnable here.
#
# It lived only in the workflow, so the way to know whether a change would pass
# was to push it and wait. That is slow, and it is easy to join a test run and a
# commit with a semicolon and not notice the tests failed.

# Pinned to the same version CI uses: a new release adding a check should not
# fail a change that has nothing to do with it.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.1

.PHONY: check fmt vet lint test build clean

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

## build: the binary Herdr runs
build:
	go build -trimpath -o bin/herdr-remote-panes .

clean:
	rm -rf bin
