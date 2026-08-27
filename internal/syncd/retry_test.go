package syncd

import (
	"errors"
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
