# Working on it

Developer notes for this repository. The README is the user-facing page: what
this plugin does, how to install it, what the settings mean, and what to do
when something looks wrong.

```bash
make check
```

That is exactly what CI runs — formatting, vet, staticcheck, the tests with the
race detector in a shuffled order, and the build — and the workflow runs the
same targets, so the two cannot drift apart.

A test written for a fix should be run against the code without the fix, or it
is a test of nothing in particular. That is the single most useful habit here
and most of the tests in this repository were written that way: break the thing
on purpose, watch the test fail, put it back.

Coverage says which lines a test ran. It does not say whether anything would
have failed had those lines been wrong, and that gap is worth checking
directly:

```bash
make mutants PKG=./internal/config
```

Each operator in the package is flipped in turn — `&&` for `||`, `>` for `>=`,
a negation dropped — and the package's own tests are run against it. Anything
that survives is a change no test would have caught, and it says which operator
on the line it changed, since a line often holds several:

```
SURVIVED  internal/syncd/daemon.go:1503:74  < -> <=  -- read before and left
            if last := d.lastPrune.Load(); last != 0 && now.Sub(...) < pruneInterval {
                                                                    ^
```

Most survivors are equivalent or unreachable, which is worth knowing too; the
ones that are neither are where the bugs have been. Two kinds are counted apart
at the end and marked on the line, so the list can be read from the top rather
than sorted by hand each time. Error branches survive until something makes that
call fail — a question about fault injection rather than about the decision on
that line, and on the packages that talk to Herdr for a living they are most of
the list. Bounds held to themselves, `if width < 8 { width = 8 }`, survive
because at the boundary the branch assigns the value already there: both
spellings agree, and no test could tell them apart. On anything that lays out a
screen they are most of the rest.

Only covered lines are tried, since a change to a line nothing runs survives by
definition.

The survivors are also written to a file in the temporary directory, which the
run names at the end. A sweep of `./internal/syncd` is two and a half hours and
the survivors are the whole of what it produces, so a terminal that keeps only
the tail of the output — a pipe into `tail`, a scrollback that has moved on —
throws them away and leaves the count. The count says how many there are and
nothing about which.

What it cannot see is worth knowing, because the report reads like a verdict on
a package rather than on operators. It changes operators, so it says nothing
about a call that passes the right values in the wrong order — and several
functions here take four booleans in a row and decide from them whether to close
somebody's terminal. Nor about what a function returns: rewriting `return ""` as
`return "", false` changes no operator and produces no mutation at all.

Both are checked by hand when the shape invites it. Swapping each pair of
adjacent same-typed arguments at a call site and running the package says
whether the order is held: three of the six pairs here are caught, and the other
three are combined with `||` or compared with `!=`, so the order genuinely does
not matter and no test could tell.

Sweeping one file of a package is much faster than sweeping all of it, and is
how a package comes to be called swept while two of its files have never been
looked at. A run that was given file names says which ones it left.

`SINCE` narrows it further, to the lines that have changed since a revision:

```bash
make mutants PKG=./internal/syncd FILES=daemon.go SINCE=v0.3.0
```

Which is the run worth making after writing something. Sweeping that file takes
an hour and a half and spends nearly all of it re-deciding mutations that were
read months ago; the forty lines that are new take four minutes. A restricted
run does not check `read.tsv` for judgements that no longer apply — a sweep of
four lines of a file leaves every other entry looking stale — so that stays a
job for a whole-file run, and for `make check`, which holds every entry to a
line that exists.

Survivors already read and decided against are recorded in
`tools/mutants/read.tsv`, with a line each saying why, and marked in the report
rather than raised again. A sweep of a package this size turns up the same dozen
equivalents every time, and reading them again costs more than the sweep does.
They are keyed by the line as written rather than by its number: edit the line
and the entry stops matching, which is right, because what was judged was the
line that was there. A fourth field names the function, for a line that appears
more than once in its file and does not mean the same thing both times — with
only the line to go on, recording one of them says the other has been read too.

What was *looked at* is a different question, and `read.tsv` cannot answer it: a
package with no survivors leaves nothing in it, so swept-and-clean and
never-swept are the same silence. `tools/mutants/swept.tsv` records each run —
package, date, and the three counts — one line per package, replaced each time.
The date is the useful part, since a package swept before the work that changed
it has not really been swept. A run given file names is recorded as the partial
sweep it was, not as the package.

A record that can only grow ends up being trusted for code that is no longer
there, so both directions are checked. A sweep names the entries whose file it
mutated but which no longer describe a survivor -- the line was edited, or a
test now catches it. `make check` is faster and blunter: it fails if an entry
names a line the tree does not contain — or names more than one, since two
identical lines share a key and a judgement about either would settle both.
They are not always the same decision.

It works on a copy under the temp directory, so an interrupted run cannot leave
a mutation in your tree, and it is not part of `make check`: it runs the tests
once per mutation, so it is minutes rather than seconds.

The daemon polls every machine while commands arrive from the menu, so its
locking is worth leaning on rather than reasoning about:

```bash
HRP_STRESS=1 go test ./internal/syncd/ -race -run UnderRealContention -count=25
```

Ten goroutines — pollers, the menu redrawing, and commands connecting and
disconnecting the same machines over each other — for eight seconds a run,
several thousand calls to Herdr each time. It is skipped without that variable
because it is minutes rather than seconds. Worth running under `GOMAXPROCS=1`
and `2` as well: a lock bug that hides at one scheduling shows up at another,
and the last one found here did.

Anything that reads what another machine said has a fuzz target, because the
input is not this plugin's to predict: a terminal's own title, a machine's
`~/.ssh/config`, the base64 frames of an observe stream, whatever Herdr printed
beside its JSON.

```bash
go test -run XXX -fuzz FuzzDecodeFrame -fuzztime 60s ./internal/mirror/
```

They hold contracts rather than outputs — a name comes back drawable and on one
line, a frame is refused or usable and never half of each, reading the same
bytes twice gives the same answer — so they keep working when the wording of
something changes. `go test ./...` runs their seed corpus; the fuzzing itself is
opt-in, like the two above.

What a reconcile pass costs is measurable the same way:

```bash
HRP_TIMING=1 go test ./internal/syncd/ -run PassCostsEveryMachine -v
```

A goroutine is started per machine, and each gives up the daemon's lock for
the round trip that asks its machine for panes, so those overlap: with a
stand-in ssh at 300ms, one machine is 310ms and so are three.

Both halves of that were paid for. Each goroutine used to hold the lock across
its own round trip, so they ran strictly one after another and a pass cost
every machine added together — three machines 1.83s. Asking each machine one
thing rather than two halved it to 910ms; overlapping the round trips took it
to 310ms, where it is the slowest machine rather than the sum.

The lock is what answers the menu, the status listing and every command, so a
pass is what the menu waits for. The test above fails if three machines cost
appreciably more than one, which is the pass having gone back to waiting on
each in turn.

## Cutting a release

A release is an annotated tag carrying its own notes, and a GitHub release made
from the same file. There is no workflow behind it: the version a build reports
comes from the commit Go recorded, not from the tag, so tagging is the whole of
it.

```bash
make check                                    # green, from a clean tree
make vuln                                     # known vulnerabilities
go test -run XXX -fuzz FuzzDecodeFrame -fuzztime 40s ./internal/mirror/
```

`make vuln` is not part of `make check`, and deliberately: it needs the network
and asks a database that changes without the code changing, so it can start
failing on a machine that has been left alone overnight — which is how a check
comes to be ignored. There are no dependencies here, `go.mod` has no require
block at all, so what it really reports on is the standard library this was
built against. That still moves, and a release is the moment it matters.

`make check` starts two daemons and hands the socket from one to the other,
because an upgrade is not an install and three releases in one day went out
broken on exactly that difference: building from a clean checkout and starting
a daemon passes every time, since a clean checkout has no daemon already
running in it. What it holds is the part that breaks — the replacing daemon
must not exit because the socket is taken, and the one it replaces must not
take the socket with it on the way out.

That check was opt-in at first, which put it in these steps and nowhere else.
Forgetting these steps is how the thing it guards got out three times, so it
runs every time now.

It does it twice: once with one build starting twice, which is a daemon
replacing itself, and once with the last release handing over to this one,
which is what an upgrade actually is. Only the second meets a daemon built
before any of the handover code existed. To check a particular jump — somebody
several versions behind, wanting to know rather than guess:

```bash
HRP_UPGRADE_FROM=v0.1.0 go test -run TestAnUpgradeFromTheLastRelease ./internal/project/
```

That needs a clone with tags, which is why CI checks out with `fetch-depth: 0`.
The test fails rather than skipping when it cannot find a release to upgrade
from: one that quietly skips is one that never runs where it matters.

It builds the tree rather than importing it, so the test cache had no idea
when the daemon changed: `internal/project` does not import `internal/syncd`,
nothing a subprocess reads is recorded, and running this by hand after editing
the daemon answered

```
ok  github.com/Poor-Plebs/herdr-remote-panes/internal/project  (cached)
```

— a pass reporting on the code as it was before the edit. Breaking the socket
handover deliberately, to check the test would notice, produced exactly that.

The tests that reach their subject through a subprocess now read the files they
depend on, which is what tells the tool those files are the input: the upgrade
reads every Go file, and the check on commands the docs give reads every test
file, since it asks `go test -list` for the names. Both now fail on the change
they are about without `-count=1`, and both still cache when nothing has moved.

`make check` was never affected, for a reason worth knowing rather than
assuming: `-shuffle=on` is not a cacheable flag, so it disables the cache for
the whole run.

Then the two places a version number is written down: the install line near the
top of the README, and `version` in `herdr-plugin.toml`. A test holds them to
each other, because neither can be held to the tag — at the moment they are
bumped, the tag they name does not exist yet. The release badge reads the latest
release itself and needs nothing. Bump both, commit, and tag *that* commit
before pushing either:

```bash
git tag -a v0.2.0 -F notes.md
git push && git push origin v0.2.0
gh release create v0.2.0 --title "v0.2.0 — ..." --notes-file notes.md
```

The notes are sections — Fixed, New, Changed, whatever the release has — and a
bullet each: one line saying what somebody upgrading would notice, and a link to
the commit it came from.
