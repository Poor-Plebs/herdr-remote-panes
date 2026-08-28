# When something looks wrong

**Installing fails with `failed to start: No such file or directory`.**

```
plugin build failed  build: 1/1
command: go build -trimpath -o bin/herdr-remote-panes .
error: failed to start: No such file or directory (os error 2)
```

That is Herdr failing to *run* `go`, not Go failing to build anything — a
compile error would come back as compiler output. The checkout worked; what is
missing is the Go toolchain on the PATH Herdr itself runs with.

The build says so itself now, and looks in the places Go is usually installed
before giving up. The message above is what v0.2.0 and earlier print.

A shell where `go version` works is not the same thing. Herdr started from a
desktop session, a login manager or a service unit inherits that environment's
PATH, which is often much shorter than the one your `.profile` builds — and Go
installed under `/usr/local/go/bin`, `~/go/bin`, Homebrew, Nix or mise lives in
exactly the part that goes missing. Check what the running Herdr has rather than
what your shell has:

```bash
# Linux
tr '\0' '\n' < /proc/"$(pgrep -n herdr)"/environ | grep '^PATH='

# macOS
ps eww -p "$(pgrep -n herdr)" | tr ' ' '\n' | grep '^PATH='
```

If Go is not installed there, install it — this plugin is built from source on
the machine it runs on. If it is installed but not on that PATH, put it where
the session Herdr starts from will find it: for most desktop sessions that means
`~/.profile` rather than `~/.bashrc`, since the latter is not read by a login
manager.

**Everything says `no running daemon`.** That is this plugin's own daemon on
*your* machine, not anything on the far end — it never got as far as SSH. The
menu shows every machine as not connected, because it has nothing to ask, and
`enter` says the same.

It runs as Herdr's `[[startup]]` hook, so it starts when Herdr does and stops
when Herdr does. Restarting Herdr is the fix, and usually the diagnosis too. If
it comes back, the log says why it did not start:

```bash
herdr plugin log list --plugin poorplebs.remote-panes
```

A `startup` entry with a non-zero `exit_code` is the daemon refusing to start,
and its `stderr` says what stopped it. A build that failed leaves no binary for
it to run at all, which the same log shows as a `build` entry.

A daemon that came up says so, once:

```
herdr-remote-panes: 21:11:17 herdr-remote-panes 200aec5 starting
herdr-remote-panes: 21:11:17 listening on /home/you/.local/state/herdr/.../control-hub.sock
```

It says nothing else for the rest of its life unless something goes wrong, so
that second line is the one to look for: `starting` without it means the socket
is what it could not get.

Both lines present and every action still saying `no running daemon` is the
rarer case: the daemon bound the socket, is still mirroring, and stopped being
able to accept connections on it afterwards. Too many open files is what does
this: the daemon reconnects every configured machine at once, each an `ssh`
with its own pipes, so a lot of machines against a low descriptor limit is a
burst. A burst clears, so it is retried for two minutes; if it does not clear,
the log says so and what to do:

```
herdr-remote-panes: 09:02:41 could not accept on the control socket: accept unix
  /home/you/.local/state/.../control-hub.sock: too many open files (retrying)
herdr-remote-panes: 09:04:41 giving up on the control socket after 2m0s -- mirroring
  continues, but no action can reach this daemon; stop it and start it again
```

Restarting Herdr is the fix. If it recurs, the descriptor limit is the lever —
`ulimit -n` in the shell Herdr starts from. Not `max_mirrors`: each mirror is
its own pane process holding its own connection, so that setting bounds
something other than what the daemon has open.

**A machine's space is missing.** You closed its terminals, and a space with
nothing in it does not exist. The machine is still connected — the menu says so
— and `enter` on it opens a terminal again.

**A machine's space is empty while mirroring.** That usually means the machine
has nothing running, since mirroring shows what is there rather than opening
anything:

```bash
ssh workbox 'herdr pane list'
```

**A terminal will not open.** Failures are printed into the pane before it
closes, and recorded:

```bash
herdr plugin log list --plugin poorplebs.remote-panes
cd ~/.local/state/herdr/plugins/poorplebs.remote-panes
cat mirror.log   # why a terminal would not open
cat daemon.log   # what the background daemon has been doing
```

`herdr plugin log list` shows a command's output once it has finished, and the
daemon does not finish, so `daemon.log` is where its side of the story is. It
begins with the build it is running, which is worth checking against the one
installed. Both files roll over rather than growing without end.

**A setting seems to be ignored.** Settings that read fine but mean something
else are reported when the daemon starts, in `status`, and in the menu:

```
config: mode "shh" is not one of ssh, attach or observe; machines default to a plain SSH terminal
```

**A machine says `unreachable, not retrying`.** It stops rather than
reconnecting forever in the background. Fix the cause, then pick it from the
menu again to retry.

How soon it stops depends on the cause. A failure that could pass on its own —
refused, timed out, no route, a machine still booting — gets a second attempt.
One that needs you gets none, because the second attempt would fail in exactly
the same way: a changed host key, a name that does not resolve, a key the
machine will not take. Those stop at once and say what to go and fix.

A machine whose terminals keep dropping stops the same way, after a couple of
replacements that did not last either.

The second attempt comes on the next poll, which is `2s` later by default, so
this is quick on purpose: a machine that is genuinely gone stops being asked
about within a few seconds rather than filling the log for the rest of the
session. The cost is that a network away for longer than that — a laptop coming
back from sleep, a VPN reconnecting — is long enough for its machines to stop
too. After a sleep it is usually all of them, and that is the case the daemon
retries on its own — 5s, 15s, 45s, up to every 5 minutes — so a lid closing
mostly sorts itself out while you are opening it.

To not wait, there is a way back for all of them at once: the connect action
with no machine named reconnects every one you have configured, given up on or
not:

```bash
herdr plugin action invoke poorplebs.remote-panes.connect
```

Worth binding to a key if you close the lid often. For one machine, pick it from
the menu and press enter, which is what "connect again to retry" means. Raising
`poll_interval` lengthens the fuse if you would rather wait than reconnect.

The most common of them is a changed host key:

```
REMOTE HOST IDENTIFICATION HAS CHANGED
```

That means the machine now presents a different key than the one recorded in
`~/.ssh/known_hosts`. A rebuilt or reinstalled machine does this, and so does
someone sitting between you and it. Check the fingerprint against the machine
itself before removing the old entry — the whole point of the warning is that
you cannot tell the two cases apart from here.

The other one worth naming is a key that never got offered:

```
too many keys offered — set IdentitiesOnly=yes for this host
```

An agent holding a dozen keys offers them in turn, and a machine that allows six
attempts refuses before reaching the one that works — so a machine you can
plainly reach with `ssh` fails here, having never been sent the right key.
Naming the key for that host in `~/.ssh/config` is the fix, and the plugin
reaches machines the same way `ssh` does, so it takes effect for both:

```
Host workbox
  IdentityFile ~/.ssh/id_workbox
  IdentitiesOnly yes
```

**The menu takes a moment to open while a machine is dropping out.** A poll
talks to each machine in turn and holds the daemon while it does, so a machine
that has stopped answering — one that swallows packets rather than refusing,
which takes the operating system's own timeout to fail — makes everything else
wait for it. The wait is bounded: ten seconds for a connection to be given up
on, thirty for a command, and after two failed passes the machine is given up
on and stops being polled at all. So it clears by itself; it is not something
to wait out for long.

Connecting to a slow machine does not do this. That path talks to the machine
before it takes the daemon, so the menu stays usable while you connect to
something that is not answering.

**The menu takes a moment to open every time, with nothing wrong.** The same
mechanism without a machine misbehaving. Machines are polled at the same time,
so a pass costs about what the slowest of them costs — but one machine slow
enough still makes a pass last longer than the gap between passes, at which
point one starts as the last ends and the daemon is never idle.

It says so when that happens, once, in `mirror.log`:

```
a pass took 2.31s, longer than the 2s between passes: machines are polled
together, so this is about what the slowest of them costs.
```

A longer `poll_interval` gives the gap back — machines are then noticed a
little later, which is the trade. So does not mirroring whichever machine is
the slow one: a plain SSH machine is never polled, so it costs nothing here
however many you have.

**A machine says `ssh` when you asked it to mirror.** Mirroring needs Herdr on
the machine, and it was not found. Usually it is simply not installed there;
`ssh` is the fallback rather than a refusal, so the machine still works.

Being off the PATH is *not* usually the cause. `ssh host <command>` does not run
a login shell, so an install under `~/.local/bin` is invisible to `command -v` —
which is why the search does not stop there. It also looks in `~/.local/bin`,
`/usr/local/bin`, Homebrew, Nix and mise. Somewhere other than those is what
`herdr_bin` is for:

```bash
ssh workbox 'command -v herdr || ls ~/.local/bin/herdr /opt/herdr/bin/herdr'
```

```json
{ "target": "workbox", "mode": "attach", "herdr_bin": "/opt/herdr/bin/herdr" }
```

Write the path out in full. It is put on the command line quoted, as any path
holding a space has to be, so `~/bin/herdr` arrives at the machine as a tilde
and `$HOME/bin/herdr` as five characters — and the machine then reports no such
file. A relative path is fine: ssh drops you in your home directory. The plugin
says so if it finds one of the other two.

`status` says which machine, and that the setting exists:

```
  bot  2 ssh  mirroring off: no herdr found on the machine — set herdr_bin if it is installed elsewhere there
```

**Clicking in the machine menu presses nothing.** It used to press whatever was
under a number nobody typed. A click reaches the menu as an escape sequence, and
in the older of the two encodings the button, column and row follow it as three
raw bytes rather than as part of the sequence — so they were read as three
keystrokes, and the byte carrying the column is the column plus 32, which for
columns 16 to 25 is a digit. Digits pick a machine and connect to it.

The menu does not turn mouse reporting off while it is up, so whether clicks
arrive at all is whatever the terminal was already doing. Anybody mirroring a
machine has it on.

**The mouse selects no text in a mirrored tab.** Fixed — but if you are on a
version before this was, here is what it was and why it looked so odd.

`herdr terminal attach`, which is how a mirrored tab is fed, turns mouse
reporting on for itself in its opening handshake: `?1000h`, `?1002h`, `?1003h`,
`?1015h`, `?1006h`, all five, before anything on the machine has asked for any
of them. `?1003h` is the worst of them, reporting every movement. With those on
your terminal gives every drag to the far side, so a drag that was only ever
going to be a selection never reaches your terminal at all — and you have to
hold whatever key it uses to take the mouse back, Ctrl in Ghostty, shift in
most others.

Which is why the same machine behaved one way through this plugin and another
way through a terminal: ssh to it and run `herdr` and you are in Herdr's own
interface, which does not do this. Only `terminal attach` does.

The plugin now drops those five, and only those five, and only from the
handshake. Anything the far side asks for afterwards is passed through
untouched — run vim or htop over there and its mouse works as it always did,
because it turns the mouse on in its own output rather than in Herdr's
handshake. So the mouse belongs to whatever is using it, and to your terminal
when nothing is.

A plain SSH terminal from this plugin was never affected: nothing sits between
it and the machine.

Neither was `observe`, which was measured rather than assumed: it sends no
mouse sequences at all. It streams the screen as rendered rather than the bytes
that drew it, so a program's mode changes are applied on the machine and never
travel — which is also why a program that wants the mouse does not get it in an
observed pane, where an attached one now does.

A pane is not left that way once the mirror ends, whichever way it ends.

**A machine says `n more in other spaces on the machine`.** In the menu the same
machine reads `connected · 1 mirrored · 3 elsewhere`. You turned mirroring on
for a machine you already work on, and one terminal arrived instead of the four
that are open there. Nothing failed: `scope` decides which of a machine's
terminals are mirrored, and it defaults to `shared` — the space this plugin
made on the machine, and nothing else. Your own terminals live in spaces of
their own there, so they are left alone.

That default is what keeps the two ends identical: the same tabs in the same
order, on both sides. Mirroring everything instead means the two sides differ,
because the machine has work on it that this one does not:

```json
{
  "scope": "all",
  "hosts": [{ "target": "workbox", "mode": "attach" }]
}
```

Like the cap below, `scope` is one setting for every machine rather than one
per machine.

**A machine says `at the mirror limit`.** One machine may mirror only so many
terminals, so that a runaway pane count on a remote cannot flood the session
here. The rest are not mirrored and are not retried: they were never attempted,
which is what makes this different from the count below. The cap is one setting
for every machine, not one per machine:

```json
{
  "max_mirrors": 64,
  "hosts": [{ "target": "workbox" }]
}
```

**A machine says `n could not be mirrored`.** Those were tried and failed —
a terminal that went away between being listed and being opened, or a machine
that stopped answering partway through a pass. Unlike the limit above, trying
again may work: connect to the machine again from the menu. The daemon's log
says what each one said.

**A machine says `more than one space has this machine's name`.** Two spaces on
that machine answer to the name this plugin looks for, so which one you get
depends on which came back first — and it need not be the same one each time,
or the same one the other end picked. Nothing fails; you simply cannot see
what is in the other space, and the count reads lower than what is there.

It happens when two machines share a hub name, or when two people point at one
machine on purpose. It cannot be prevented from one side, so it is reported
rather than fixed: rename the spare space on the machine, or give this machine a
name of its own to look for.

Like the cap above, this names the space on every machine you mirror rather than
on one of them, so pick something that reads as coming from *here*:

```json
{
  "remote_workspace_format": "from my laptop",
  "hosts": [{ "target": "workbox" }]
}
```

**Terminals on a machine keep disappearing.** Two machines answering to the
same name share one space, and each pass reads the other's terminals as panes
that wandered in and closes them. Connecting both leaves one of them with
nothing. The menu, `status` and the daemon's log all say so:

```
hosts "bot" and "ci" are both called "build", so they would share one space and close each other's terminals; give one of them its own label
```

A machine answers to its `label`, or to its target when it has none, so this
also happens when one machine's label is another machine's target.

**A change to `config.json` has not taken effect.** The daemon reads that file
when it starts, so edits apply from the next time Herdr starts. Toggling
mirroring from the menu also rereads it, which is a quick way to apply an edit
— though it applies every other pending edit in the file at the same time.


**An update does not seem to have changed anything.** Installing replaces the
files on disk but leaves the already-running daemon alone, so its fixes take
effect when Herdr next starts. `status` says so when it notices:

```
warning: the running daemon is 427e2ad but 9fcc667 is installed;
restart Herdr to pick up the update
```

To see both halves at once, ask which build is which:

```console
$ herdr-remote-panes version
herdr-remote-panes 9fcc667
daemon             427e2ad
```

The second line is the one that matters: that is the build reconciling your
panes. It reads `not running` when nothing answers, which is itself an answer,
and `unknown` when a daemon answers but has no commit to name — what `go run`
and a test binary look like. That is not the same as an old daemon, and the
warning under it says so rather than guessing which of the two it is.

**A keybinding does nothing.** First check the config itself is being read:

```bash
herdr config check
```

If that is happy, the binding probably clashes with a built-in, which wins
silently. Taken:
`b c e g h j k l n o p q r s v w x z tab minus alt+g shift+d shift+g shift+n
shift+p shift+r shift+t shift+w shift+x shift+tab`.
