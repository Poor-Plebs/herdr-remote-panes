package syncd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWhatAWholeFailedPassMeans(t *testing.T) {
	// One machine failing while the others are fine says something about that
	// machine. Every machine failing in the same pass says something about the
	// link at this end -- a laptop waking up is the ordinary case, and it cost
	// every machine at once, each needing an explicit connect to come back.
	for _, tt := range []struct {
		what  string
		hosts []passResult
		want  bool
	}{
		{"nothing connected", nil, false},
		{"the only machine went down", []passResult{{gaveUp: true}}, true},
		{"every machine went down", []passResult{{gaveUp: true}, {gaveUp: true}, {gaveUp: true}}, true},
		{"one machine went down", []passResult{{gaveUp: true}, {}, {}}, false},
		{"all but one went down", []passResult{{gaveUp: true}, {gaveUp: true}, {}}, false},
		// A changed host key does not clear on its own. Retrying it spins, and
		// a pass holding one is not the link going away.
		{"every machine down, one for good", []passResult{{gaveUp: true}, {gaveUp: true, settled: true}}, false},
		{"the only machine, for good", []passResult{{gaveUp: true, settled: true}}, false},
	} {
		if got := planWholePassRetry(tt.hosts); got != tt.want {
			t.Errorf("%s: planWholePassRetry = %v, want %v", tt.what, got, tt.want)
		}
	}
}

func TestTheRetryBacksOffAndStopsGrowing(t *testing.T) {
	// The two cases are far apart: a link back in seconds, and one not back for
	// an hour. Starting short catches the first; the ceiling stops the second
	// being a machine that reconnects every few seconds all afternoon.
	want := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second,
		135 * time.Second, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for step, expected := range want {
		if got := planAutoRetryWait(step); got != expected {
			t.Errorf("retry %d waits %s, want %s", step, got, expected)
		}
	}
	// However long it has been down, it never stops trying and never grows
	// past the ceiling -- a laptop shut for a weekend still comes back.
	if got := planAutoRetryWait(500); got != 5*time.Minute {
		t.Errorf("after 500 retries the wait is %s, want the ceiling", got)
	}
}

// downHost is a machine that has been given up on, with a reason that could
// clear on its own.
func downHost(target string) *hostSync {
	state := newTestHost()
	state.host.Target = target
	state.gaveUp = true
	state.lastErr = errors.New("ssh: connect to host bot port 22: Network is unreachable")
	return state
}

func TestEveryMachineGoingDownTogetherIsRetriedWithoutBeingAsked(t *testing.T) {
	// Closing a lid failed every machine in the same pass, each counted its own
	// failures to the limit, and each was given up on. Coming back needed an
	// explicit connect per machine, for something that was never about them.
	d := New(machineConfig("bot", "workbox"))
	states := []*hostSync{downHost("bot"), downHost("workbox")}
	for _, state := range states {
		d.hosts[state.host.Target] = state
	}

	d.scheduleWholePassRetry(states)

	for _, state := range states {
		if state.linkRetryAt.IsZero() {
			t.Fatalf("%s was left waiting to be connected to by hand", state.host.Target)
		}
		if wait := time.Until(state.linkRetryAt); wait > 6*time.Second {
			t.Errorf("%s tries again in %s, want the first step of five seconds",
				state.host.Target, wait)
		}
	}

	// Running again before the retry is due must not push it away. This runs
	// every pass -- a couple of seconds apart -- so rescheduling a machine that
	// is already counting down would move the deadline for ever and it would
	// never arrive.
	was := states[0].linkRetryAt
	d.scheduleWholePassRetry(states)
	if !states[0].linkRetryAt.Equal(was) {
		t.Errorf("a second pass moved the retry from %s to %s: it would never arrive",
			was, states[0].linkRetryAt)
	}
	if step := states[0].linkRetryStep; step != 1 {
		t.Errorf("a second pass counted another retry: step is %d, want 1", step)
	}
}

func TestOneMachineFailingIsStillLeftAlone(t *testing.T) {
	// The whole distinction. A machine that cannot be reached while the others
	// are fine has something wrong with it, and retrying it on a timer would
	// reconnect to a machine somebody turned off, all day.
	d := New(machineConfig("bot", "workbox"))
	down, fine := downHost("bot"), newTestHost()
	fine.host.Target = "workbox"
	d.hosts["bot"], d.hosts["workbox"] = down, fine

	d.scheduleWholePassRetry([]*hostSync{down, fine})

	if !down.linkRetryAt.IsZero() {
		t.Errorf("a single machine failing was scheduled to retry at %s; it waits "+
			"to be connected to", down.linkRetryAt)
	}
}

func TestAMachineThatAnsweredStartsTheBackoffOver(t *testing.T) {
	// Otherwise a machine that has been through one long outage starts the next
	// one at the ceiling: down for a weekend in March, and in April a link that
	// blinks takes five minutes to come back rather than five seconds.
	state := newTestHost()
	state.linkRetryStep = 4
	state.linkRetryAt = time.Now().Add(time.Minute)

	recordReconcile(state, nil)

	if state.linkRetryStep != 0 || !state.linkRetryAt.IsZero() {
		t.Errorf("a machine that answered still carries step %d and a retry at %s",
			state.linkRetryStep, state.linkRetryAt)
	}
}

// givenUp connects a machine for real and then puts it in the state a laptop
// waking leaves it in. Connecting rather than assembling the state by hand
// because a machine in the daemon's map has always been connected to at some
// point -- that is the only way into it -- and a hand-made one has no client,
// which is a crash the daemon cannot reach.
func givenUp(t *testing.T, d *Daemon, target string) *hostSync {
	t.Helper()
	if reply := d.dispatch(Command{Cmd: "connect", Host: target}); !reply.OK {
		t.Fatalf("connect %s: %s", target, reply.Message)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.hosts[target]
	if state == nil {
		t.Fatalf("%s did not end up connected", target)
	}
	state.gaveUp = true
	state.failCount = maxHostAttempts
	return state
}

func TestAPassWhereEveryMachineFailedWritesTheRetryDownItself(t *testing.T) {
	// The other half of the join. The test below proves a retry already
	// written down is taken by the pass; the ones above prove the scheduler
	// writes one when asked. Neither asks whether the pass ever calls the
	// scheduler, and deleting that one line from reconcileOnce broke nothing
	// in this package: every test here either calls the scheduler itself or
	// sets linkRetryAt by hand first.
	//
	// What is left without it is the case the whole mechanism is for. A lid
	// closing or a VPN dropping fails every machine in one pass, each counts
	// its own failures to the limit and is given up on, and nothing writes
	// down a time to try again -- so coming back needs an explicit connect for
	// every machine, for something that was never about them. The README and
	// the troubleshooting page both promise it retries itself.
	withFakeHerdr(t)
	d := New(machineConfig("bot", "workbox"))

	// Given up on, and nothing scheduled by hand: the pass has to do it.
	states := []*hostSync{givenUp(t, d, "bot"), givenUp(t, d, "workbox")}
	d.mu.Lock()
	for _, state := range states {
		if !state.linkRetryAt.IsZero() {
			t.Fatalf("%s already had a retry written down, so this would prove "+
				"nothing", state.host.Target)
		}
	}
	d.mu.Unlock()

	d.reconcileOnce()

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, state := range states {
		if state.linkRetryAt.IsZero() {
			t.Errorf("%s came out of a pass that failed every machine with no "+
				"time to try again, so it waits to be connected to by hand",
				state.host.Target)
		}
	}
}

func TestATakenRetryHandsBackAFullBudgetOfAttempts(t *testing.T) {
	// A machine given up on when everything was down gets another go without
	// being asked, and it gets it with a full budget of attempts as though it
	// had just been connected to. Handing back the budget is what makes the
	// retry worth taking: carrying the old count means the first failure gives
	// up again instantly and the retry bought nothing.
	//
	// Nothing held that. The test below sets the retry by hand and watches it
	// be taken; it never asks what the machine is given when it is.
	withFakeHerdr(t)
	d := New(machineConfig("bot", "workbox"))

	states := []*hostSync{givenUp(t, d, "bot"), givenUp(t, d, "workbox")}
	d.mu.Lock()
	due := time.Now().Add(-time.Second)
	for _, state := range states {
		state.linkRetryAt = due
	}
	d.mu.Unlock()

	// Still down when the retry comes round, which is what makes this the case
	// the clearing is for rather than the one below.
	sshFails(t, "ssh: connect to host bot port 22: Connection refused")

	d.reconcileOnce()

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, state := range states {
		if state.failCount >= maxHostAttempts {
			t.Errorf("%s has %d failures counted, which is the limit: the retry "+
				"handed back nothing and the next failure gives up again",
				state.host.Target, state.failCount)
		}
	}

	// Not asserted: that taking the retry clears the time it was due at. That
	// line cannot be reached from here -- a pass that ends well clears the
	// time on the success path whatever the retry did, and a pass that ends
	// badly for these machines is what this fixture could not arrange: ssh
	// failing does not make the pass fail for them. Deleting the clearing
	// leaves this test green, and an assertion about it here would be one that
	// cannot fail. It is still unheld, and saying so beats pretending.
	_ = due
}

func TestADueRetryIsActuallyTakenByThePass(t *testing.T) {
	// The half that matters. Scheduling a retry and skipping the machine
	// anyway is two correct pieces either side of a decision nothing joins:
	// the tests above prove a time gets written down, and a machine that is
	// still skipped when it arrives is exactly as stuck as before.
	withFakeHerdr(t)
	d := New(machineConfig("bot", "workbox"))

	due, waiting := givenUp(t, d, "bot"), givenUp(t, d, "workbox")
	d.mu.Lock()
	// One whose retry has come round, and one still counting down.
	due.linkRetryAt = time.Now().Add(-time.Second)
	waiting.linkRetryAt = time.Now().Add(time.Hour)
	d.mu.Unlock()

	d.reconcileOnce()

	d.mu.Lock()
	defer d.mu.Unlock()
	if due.gaveUp {
		t.Error("the machine whose retry came round was skipped again: the time " +
			"was written down and never read, so nothing changed")
	}
	if !due.linkRetryAt.IsZero() {
		t.Errorf("a retry that was taken is still scheduled for %s, so it would "+
			"be taken again every pass from now on", due.linkRetryAt)
	}
	// A fresh budget of attempts, as though it had just been connected to.
	// Carrying the old count means the first failure gives up instantly and
	// the retry is worth nothing.
	if due.failCount >= maxHostAttempts {
		t.Errorf("the retry started with %d failures already counted, which is "+
			"the limit: one failure and it gives up again", due.failCount)
	}

	if !waiting.gaveUp {
		t.Error("a machine whose retry has not come round yet was tried anyway")
	}
	if waiting.linkRetryAt.IsZero() {
		t.Error("a machine still counting down had its retry cleared")
	}
}

func TestAMachineGivenUpOnAloneIsNeverRetriedByThePass(t *testing.T) {
	// Nothing schedules a retry for it, so nothing must take one: a machine
	// with no time written down waits to be connected to, which is what it did
	// before any of this.
	withFakeHerdr(t)
	d := New(machineConfig("bot"))

	alone := givenUp(t, d, "bot")

	d.reconcileOnce()

	d.mu.Lock()
	defer d.mu.Unlock()
	if !alone.gaveUp {
		t.Error("a machine with no retry scheduled was tried anyway")
	}
}

func TestOneMachineDownIsNotReportedAsAllOfThem(t *testing.T) {
	// The whole-pass retry is planned for one machine too: the planner asks
	// whether every machine gave up, and where there is one machine, one is
	// every one. The line it printed then was "all 1 machines went down
	// together, which is usually this end rather than them" -- ungrammatical,
	// and drawing an inference it has no grounds for. Several machines failing
	// at once is what says the fault is at this end; one machine failing says
	// nothing of the kind, and the machine is the likelier suspect.
	logged := captureLog(t)
	d := New(machineConfig("bot"))
	state := downHost("bot")
	d.hosts[state.host.Target] = state

	d.scheduleWholePassRetry([]*hostSync{state})

	said := logged.String()
	if strings.Contains(said, "1 machines") {
		t.Errorf("one machine is counted in the plural: %q", said)
	}
	if strings.Contains(said, "rather than them") {
		t.Errorf("one machine going down was blamed on this end, which one machine "+
			"cannot show: %q", said)
	}
	// It still has to say the thing it is for: that the machine is coming back
	// on its own, and when.
	if !strings.Contains(said, "5s") {
		t.Errorf("the log does not say when it will try again: %q", said)
	}
	if !strings.Contains(said, "bot") {
		t.Errorf("the log does not say which machine it is about: %q", said)
	}
}

func TestTheRetrySaysItIsGoingToHappen(t *testing.T) {
	// The machines still show as unreachable while they wait, so the log line
	// is the only thing that distinguishes "coming back on its own in five
	// seconds" from "sitting there until you press something". Nothing held
	// it: a sweep found that inverting when it is said changes no test.
	logged := captureLog(t)
	d := New(machineConfig("bot", "workbox"))
	states := []*hostSync{downHost("bot"), downHost("workbox")}
	for _, state := range states {
		d.hosts[state.host.Target] = state
	}

	d.scheduleWholePassRetry(states)

	said := logged.String()
	if !strings.Contains(said, "went down together") {
		t.Errorf("scheduling a retry said nothing about it: %q", said)
	}
	if !strings.Contains(said, "5s") {
		t.Errorf("the log does not say when it will try again: %q", said)
	}
	// Once for the pass, not once per machine: every machine is the case this
	// is about, so a line each is the same sentence n times.
	if n := strings.Count(said, "went down together"); n != 1 {
		t.Errorf("said it %d times for one pass, want once", n)
	}

	// And nothing more on the passes that follow, which run every couple of
	// seconds while the machines are still waiting. Saying it again each time
	// would bury it, and would also be untrue: nothing new was scheduled.
	before := len(logged.String())
	for i := 0; i < 3; i++ {
		d.scheduleWholePassRetry(states)
	}
	if got := logged.String(); len(got) != before {
		t.Errorf("later passes said it again: %q", got[before:])
	}
}

func TestTheRetryNamesTheSoonestWait(t *testing.T) {
	// Machines need not be waiting the same length of time: one that answered
	// since the last outage starts its backoff again, so it can be waiting five
	// seconds while another that has been down all along waits five minutes.
	// Naming whichever came last in the loop would report a wait nothing is
	// doing -- and the order machines come in is not something a reader
	// controls.
	// Both orders. The machines come out of a slice, and with the soonest
	// second the first one sets the answer and the second corrects it -- which
	// is the case that passes whether the first is handled properly or not.
	// With the soonest first, nothing corrects a wrong start.
	for _, tt := range []struct {
		what  string
		first string
	}{
		{"the soonest last", "weathered"},
		{"the soonest first", "fresh"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			logged := captureLog(t)
			d := New(machineConfig("bot", "workbox"))
			fresh, weathered := downHost("bot"), downHost("workbox")
			weathered.linkRetryStep = 4 // at the ceiling
			states := []*hostSync{weathered, fresh}
			if tt.first == "fresh" {
				states = []*hostSync{fresh, weathered}
			}
			for _, state := range states {
				d.hosts[state.host.Target] = state
			}

			d.scheduleWholePassRetry(states)

			said := logged.String()
			if !strings.Contains(said, "in 5s") {
				t.Errorf("with one machine due in 5s and another in 5m, the log says %q", said)
			}
			// Each still gets its own wait; only what is said is the soonest.
			if wait := time.Until(weathered.linkRetryAt); wait < 4*time.Minute {
				t.Errorf("the machine at the ceiling was rescheduled for %s, want its own wait", wait)
			}
		})
	}
}

func TestASnapshotThatCannotBeReadIsSaidOutLoud(t *testing.T) {
	// An empty snapshot means nothing was dismissed, so a machine's terminals
	// are all mirrored again -- including the ones somebody closed. That is
	// right when there is no snapshot, which is the first run. When there is
	// one and it cannot be read, the same thing happens and looks like closed
	// terminals coming back on their own.
	for _, tt := range []struct {
		what    string
		write   func(t *testing.T, path string)
		saysSo  bool
		because string
	}{
		{
			what:   "no snapshot at all",
			write:  func(*testing.T, string) {},
			saysSo: false, because: "the first run is not a problem to report",
		},
		{
			what: "a snapshot that is not JSON",
			write: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("{ this is not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			saysSo: true, because: "terminals will come back and nothing else says why",
		},
	} {
		t.Run(tt.what, func(t *testing.T) {
			t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
			t.Setenv("HERDR_SESSION", "snap")
			path, err := snapshotPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			tt.write(t, path)

			logged := captureLog(t)
			got := loadSnapshot()

			if len(got.Hosts) != 0 {
				t.Errorf("loaded %d machines from a snapshot that has none", len(got.Hosts))
			}
			if said := logged.String() != ""; said != tt.saysSo {
				t.Errorf("said %v, want %v: %s (log: %q)", said, tt.saysSo, tt.because,
					logged.String())
			}
		})
	}
}
