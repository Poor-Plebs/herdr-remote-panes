# herdr-remote-panes

[![CI](https://github.com/Poor-Plebs/herdr-remote-panes/actions/workflows/ci.yml/badge.svg)](https://github.com/Poor-Plebs/herdr-remote-panes/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/Poor-Plebs/herdr-remote-panes?logo=go&label=Go)](go.mod)
[![License](https://img.shields.io/github/license/Poor-Plebs/herdr-remote-panes)](LICENSE)
[![Release](https://img.shields.io/github/v/release/Poor-Plebs/herdr-remote-panes?sort=semver&label=release)](https://github.com/Poor-Plebs/herdr-remote-panes/releases/latest)

Work on other machines from the [Herdr](https://herdr.dev) in front of you.

Press a key, pick a machine, and a terminal on it opens here — in its own space
in the sidebar, named after the machine. It is an ordinary SSH session, so
nothing has to be installed at the far end, and the machines are read from your
`~/.ssh/config`, so there is usually nothing to configure either.

Herdr cannot push panes from one machine to another, so this pulls: a small
daemon runs beside your session, keeps one reused SSH connection per machine,
and opens a local pane for each terminal that should be showing.

That is the whole of it by default. There is also an experimental mode that
keeps both ends in step — [mirroring](#mirroring-experimental) — which needs
Herdr on the machine too.

Your sidebar ends up looking like this, one space per machine:

```
☁  workbox         a machine you can reach
   shell@workbox   a terminal on it
   build@workbox   another one
⚠  ci              a machine that is not answering
~                  your own local space, untouched
```

## Install

```bash
herdr plugin install Poor-Plebs/herdr-remote-panes
```

Herdr builds it from source, so this machine needs Go. The machines themselves
need nothing but an SSH server you can already reach.

That takes the latest commit. To pin a release instead:

```bash
herdr plugin install Poor-Plebs/herdr-remote-panes --ref v0.4.1
```

Updating is the same command again — there is no separate update. Herdr shows
what it is about to install, including the version and the commit, and asks
before replacing what is there. The daemon already running is left alone, so
the new build does nothing until Herdr restarts; `herdr-remote-panes version`
says which build is which.

Then bind the menu to a key in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+shift+s"
type = "plugin_action"
command = "poorplebs.remote-panes.menu"
description = "connect to a machine"
```

Pick keys Herdr does not already use — a clash is silent, and the built-in wins.
`prefix+shift+r`, for instance, is `reload_config`.

## The menu

The key you bound opens this:

```
  Connect to a machine

 > 1. workbox     connected · 2 open
   2. ci          unreachable · host key changed · enter to retry
   3. buildbox    unreachable · connection refused · mirrored · enter to retry
   4. gh-runner   from ~/.ssh/config · ssh

  ↑↓ jk move · pgup/pgdn g/G jump · 1-9 pick · enter connect
  d disconnect · m toggle mirroring (experimental) · q cancel
```

Every machine you can reach is listed, whether or not this plugin knows about
it: the ones you have configured come first, then everything else in your
`~/.ssh/config`. Each says how it is reached and how it is doing.

`enter` connects and gives you a terminal — on a machine that has been given up
on, it is also how you say "try again now". `d` closes a machine's panes here,
leaving the work on the machine running, so `enter` brings it straight back.
`m` turns [mirroring](#mirroring-experimental) on or off for the machine under
the cursor, and asks first if that would close terminals you have open on it.
The list scrolls, so a long SSH config is fine.

`m` is also the one key here that writes to your config: the mode has to be
remembered somewhere, so a machine that was only in `~/.ssh/config` is added to
`config.json` when you toggle it. It then counts as configured — it sorts to the
top of the menu, and reconnecting everything reconnects it too.

## Everyday use

| You want | Do this |
| --- | --- |
| A terminal on a machine | Open the menu, pick it |
| Another terminal on it | Open a tab while in its space |
| That machine's space back | Open the menu, pick it again |
| To close a machine's panes | Open the menu, press `d` |
| To see what is connected | `herdr plugin action invoke poorplebs.remote-panes.status` |

Picking a machine takes you to its space, whether it had just been connected or
was already there, and opening a terminal on one leaves you in the terminal. A
pane that opens on its own — a dropped link coming back, or a terminal
appearing on a mirrored machine — never takes the screen from you.

Opening a tab inside a machine's space gives you one **on that machine** — use
the plugin's own new-tab action for that. Herdr's own new-tab key and the plus
icon always open a local shell, and no plugin can intercept them. On a mirrored
machine such a pane is moved onto the machine a moment after; on a plain SSH one
it is left where it is, so a tab opened that way stays a local shell sitting in
the machine's space. Binding the new-tab key to the plugin avoids the question
either way:

```toml
[keys]
new_tab = ""          # unset the built-in first, or it wins

[[keys.command]]
key = "prefix+c"
type = "plugin_action"
command = "poorplebs.remote-panes.open-tab"
description = "new tab, on the machine you are looking at"
```

Outside a machine's space both actions open an ordinary local pane or tab, so
they are safe to bind over your usual keys.

## Mirroring (experimental)

By default a machine gives you a plain SSH terminal. Nothing is discovered or
kept in step, it needs nothing on the far side, and there is little to go wrong.

Mirroring keeps both ends showing **the same terminals**: open one on the
machine and it appears here, close a tab here and it closes there. Agents
running over there show in your sidebar with the right name and status. It needs
Herdr on the machine and has considerably more moving parts, which is why it is
off by default.

Turn it on for a machine with `m` in the menu, or in the config:

```json
{ "target": "workbox", "mode": "attach" }
```

`observe` mirrors read-only instead, which any number of machines can do to the
same terminal at once. `attach` is exclusive: two machines mirroring the same
terminal will fight over it.

`m` will not change a machine set to `observe`. The key only knows two states,
off and `attach`, so it has no way to give `observe` back — it used to toggle
such a machine off and then to `attach`, which turned watching quietly into
taking the terminal from whoever had it, with no way back from the menu. Now it
says so and changes nothing. Edit the config to change it.

Those machines say `read-only` where the others say `mirrored`, so you can tell
before you type into one:

```
  1. workbox     connected · 3 read-only
  2. buildbox    connected · 2 mirrored
```

### What gets mirrored

The terminals in one shared space, named after *your* machine, so both ends show
the same tabs in the same order — tab 1 here is tab 1 there. Whatever else the
machine has running stays in its own spaces, private and untouched.

To see them from the machine itself:

```bash
ssh workbox
herdr
```

Set `"scope": "all"` to mirror everything the machine has instead, including
work started there. The two sides will then differ by whatever lives in its
other spaces.

## Things worth knowing

**A machine's tabs are not tabs here.** Mirrored terminals are placed by
`placement`, which defaults to `split`: the first opens a tab in the machine's
space and every one after it splits that tab. So a machine you have three tabs
open on arrives as one tab here with three panes in it, and it looks like it
happened gradually because it does — each terminal splits in as it appears.

The order is kept even though the shape is not: what is first there is first
here. For a tab each instead:

```json
{
  "placement": "tab",
  "hosts": [{ "target": "workbox", "mode": "attach" }]
}
```

`split` puts everything in one tab, which is what this used to do, and `zoomed`
gives a tab each with the newest filling the space. Any of them can be set per
machine with `hosts[].placement`, which is worth doing when only one of your
machines has enough open on it to matter.

A machine in plain `ssh` mode has no arrangement to follow — its terminals are
opened here rather than mirrored from there — so `follow` splits them, which is
what they did before.

A terminal *you* open with the new-tab action is different: it keeps the tab you
asked for, including when its pane has to be opened again — a link that dropped,
a Herdr that restarted. It used to keep it only until then, and came back placed
the machine's usual way, so a set of tabs turned into one tab with all of them
split inside it some time after you made them.

**Closing a mirrored tab closes the terminal on the machine.** Mirroring is
two-way, and closing is part of it: the tab goes here and the terminal goes
there, with whatever was running in it. That is `close_propagates`, and it is
on by default — the alternative surprises people the other way, leaving work
running on a machine with nothing here to show it.

Disconnecting is the one that does not. `d` in the menu, or the disconnect
action, closes a machine's panes here and leaves everything on the machine
alone, which is why `enter` brings it straight back with the work still in it.
So: `d` to put a machine away, and closing tabs to finish with what is in them.

If you would rather closing a tab left the machine alone:

```json
{
  "close_propagates": false,
  "hosts": [{ "target": "workbox", "mode": "attach" }]
}
```

**A dropped connection comes back; one you closed does not.** A terminal whose
SSH link fails is reopened. One you deliberately closed stays closed, and is
still closed after a restart — including one you ended by typing `exit` after
something went wrong, which leaves the session with that command's status
rather than zero.

A link that keeps dropping is a different thing. If a replacement terminal does
not last either, the machine is left alone rather than having a pane opened and
shut every couple of seconds for the rest of the session. It shows as
unreachable, and picking it from the menu is how you say "try now".

**Restarting Herdr brings your machines back.** Each one you had connected
reconnects with as many terminals as it had, opened one at a time rather than
all at once. They are new sessions, though: a plain SSH terminal's shell goes
when its pane does, so anything running in one is lost unless you started it
under something that outlives it. With mirroring the work is on the machine
rather than in the pane, so it is still running and the mirror simply finds it
again.

**Turning mirroring on or off leaves you with one terminal, whatever you had.**
The menu asks first when that costs something — turning it *on* closes plain
SSH terminals, and a plain SSH terminal's shell goes when its pane does. Turning
it off costs nothing to check, because the work is on the machine.

The two modes are nothing alike underneath, so the machine's panes here are
dropped and it is connected again in the new way — and connecting to a machine
opens a terminal, the same as picking it from the menu does. What that costs
depends on which way you went: work on a mirrored machine is on the machine and
is still there afterwards, while a plain SSH terminal's shell goes when its pane
does, so anything running in one of those is lost. Worth finishing what you are
doing first.

**Editing the config takes effect on the next pass**, within a couple of
seconds, with no restart. It did not until v0.3.2: the daemon read the file
when it started and again only as a side effect of pressing `m`, so every other
setting was fixed for the session while the file plainly said otherwise. If you
set `placement` to `tab` and watched terminals keep arriving as splits, that is
what it was.

A file caught half-written is not read — saving is not atomic in every editor,
and a pass comes round every couple of seconds — so the settings in use stay
the ones that last parsed, and the log says so.

**The menu waits for a slow machine.** A reconcile pass holds the daemon's lock
while it asks each machine what it has open, and answering the menu needs that
same lock — so opening the menu, or invoking any action, waits for whatever the
pass is doing. Measured: a machine taking two seconds to answer made a status
request take 5.7. The machines are asked one after another rather than at once,
so it grows with how many you have, and `ssh` is given `ConnectTimeout=10`,
which is the worst case per machine.

A machine that has been given up on is skipped, so this settles down on its own
once an unreachable one stops being retried. It is worst in the seconds after a
machine goes.

**An unreachable machine is left alone** rather than retried forever in the
background. How soon depends on the cause: something that might pass on its own
gets a second try, and something that needs you — a changed host key, a name
that does not resolve, a key the machine will not take — gets none, because the
second try would fail in exactly the same way. Fix whatever is wrong and pick
the machine from the menu, which is how you say "try now".

**Unless every machine goes at once**, which is not about the machines. A lid
closing or a VPN dropping fails all of them in the same pass, so that case
retries itself: 5s, then 15s, 45s, and on up to every 5 minutes for as long as
it takes. They still show as unreachable in the meantime, and you can still
press enter on one to try it now. A machine that fails while the others are
fine has something wrong with *it* and waits for you, as above — and if any of
them failed for a reason that will not clear on its own, none of it retries,
because that is not a link that went away.

**Who else can see your space on a machine.** The space this plugin makes there
is named after *your* machine, so two people mirroring the same remote each get
their own and neither mirrors the other's — that is what `scope` being `shared`
means. It is not privacy, though. The space lives in that machine's Herdr, under
the account you log in as, so anyone who can be that user there sees it listed
and can open the terminals in it. A different Unix account cannot: the socket is
readable only by its owner. And anyone mirroring the machine with `"scope":
"all"` mirrors everything on it, yours included.

One way to end up sharing without meaning to: the name comes from your
hostname, so two machines called the same thing — two `localhost`s, or a pair of
laptops from one company image — land in the same space and mirror each other's
terminals. With `attach` they will take them from each other. Give one of them a
different `remote_workspace_format` if that happens.

Sharing a space with someone *on purpose* — both of you working in it, each
driving the terminals you opened — is a planned feature rather than a supported
one. The design, and what already works today if you want to try it by hand, is
in [docs/pairing.md](docs/pairing.md).

**Machines without Herdr just work.** They get a plain SSH terminal. Mirroring
is the only part that needs Herdr at both ends, and a machine that turns out not
to have it falls back rather than refusing to connect.

**An agent on a machine shows in the sidebar only if the machine is mirrored.**
Start Claude in a plain SSH terminal and what Herdr has here is a terminal: the
agent's name, and whether it is working or waiting for you, are things the
machine's own Herdr knows, and mirroring is what asks it. Without mirroring
there is nothing to ask. Turn it on for that machine with `m` and the agent
appears under its own name, with its state, and keeps up as that changes.

**Terminals you open on a machine stay on it.** Disconnecting closes the panes
here, not the work there — a build running in one keeps running, which is what
lets you reconnect and pick up where you left off. It also means they accumulate
on the machine if you never look:

```bash
ssh workbox 'herdr pane list'          # what is still open there
ssh workbox 'herdr pane close wG:p3'   # close one you are done with
```

## Settings

All optional, in `$(herdr plugin config-dir poorplebs.remote-panes)/config.json`:

```json
{
  "hosts": [
    { "target": "workbox" },
    { "target": "ci", "mode": "attach" }
  ]
}
```

| Setting | Default | What it does |
| --- | --- | --- |
| `hosts[].target` | – | The machine, as you would type after `ssh` |
| `hosts[].mode` | `ssh` | `ssh` plain terminal; `attach` or `observe` to mirror. The menu's `m` sets only `ssh` or `attach`, and refuses a machine set to `observe` |
| `hosts[].label` | the target | How it is named here. Must differ from every other machine's: it names the machine's space, and two machines sharing one would take each other's terminals |
| `hosts[].disabled` | `false` | Skip it without removing it |
| `hosts[].session` | the global one | Which Herdr session on *this* machine |
| `hosts[].placement` | the global one | How *this* machine's terminals are placed |
| `hosts[].workspace` | the global one | Which space *this* machine's terminals land in |
| `hosts[].herdr_bin` | found automatically | Where `herdr` lives on *this* machine |
| `mode` | `ssh` | Default mode for machines that do not set one |
| `placement` | `follow` | How terminals are placed here. `follow` puts them where the machine has them; `split`, `tab` and `zoomed` override that |
| `workspace_format` | `☁  {host}` | How a machine's space is named |
| `workspace_format_down` | `⚠  {host}` | How it is named while unreachable |
| `workspace` | one per machine | Put every machine in one space instead |
| `remote_workspace_format` | `☁  {hub}` | How *this* machine's space is named **on** the machine, so it is recognisable from that end |
| `label_format` | `{name}@{host}` | How its terminals are named. `{name}` `{host}` `{agent}` `{pane}` |
| `poll_interval` | `2s` | How often machines are checked. Needs a unit — `30s`, not `30` — and cannot go below `500ms`; anything else falls back to the default, and the plugin says it did |
| `close_propagates` | `true` | Closing a mirrored tab closes it on the machine |
| `capture_new_panes` | `true` | Move a local pane opened in a machine's space onto it |
| `auto_start` | `true` | Start Herdr on the machine when mirroring needs it |
| `scope` | `shared` | `shared` mirrors the shared space; `all` mirrors everything |
| `session` | `default` | Which Herdr session on the machine is shared |
| `max_mirrors` | `32` | Most terminals to mirror per machine. Past it the rest are left alone, and `status` says so. Zero is not "no limit" — it is not a cap at all, so the default goes back, and the plugin says it did |
| `takeover` | `true` | Take over a stale connection left by a closed terminal |
| `herdr_bin` | found automatically | Where `herdr` lives on the machine |

## When something looks wrong

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

## How it works

The daemon starts with your session and looks every two seconds at the panes
Herdr is holding, opening and closing them so that what you see here matches
what should be there.

That loop is about this machine, not the others. A plain SSH pane simply runs
`ssh`, and the loop never talks to the machine at all — which is why that mode
needs nothing installed on it, and why a machine that goes away is noticed by
its terminal dying rather than by being asked.
Only a mirrored machine is polled over SSH, for the list of terminals to keep
in step, over one reused connection.

Connecting is the exception: it talks to a plain SSH machine once, to check it
answers. Without that an unreachable one reported `ok`, and the trouble only
showed up later as terminals that would not stay open.

A mirrored terminal is bridged with Herdr's own direct terminal attach, so it is
a live terminal rather than a picture of one: what you type goes to the process
on the machine.

Requires Herdr 0.8.0+ and Go 1.25+, on Linux or macOS.

## Working on it

```bash
make check
```

That is exactly what CI runs — formatting, vet, staticcheck, the tests with the
race detector in a shuffled order, and the build — and the workflow runs the
same targets, so the two cannot drift apart.

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

### Cutting a release

A release is an annotated tag carrying its own notes, and a GitHub release made
from the same file. There is no workflow behind it: the version a build reports
comes from the commit Go recorded, not from the tag, so tagging is the whole of
it.

```bash
make check                                    # green, from a clean tree
go test -run XXX -fuzz FuzzDecodeFrame -fuzztime 40s ./internal/mirror/
```

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

Then the two places a version number is written down: the install line near the
top of this README, and `version` in `herdr-plugin.toml`. A test holds them to
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

## Trust

This runs with your privileges and connects by SSH to machines you name. Read
the source before installing it, the same as any Herdr plugin.

`connect` with no machine given falls back to the text selected in the terminal,
so a name can be highlighted and connected to. Selected text is not necessarily
something you wrote, so it is checked before it is used: anything ssh would read
as an option rather than a machine — a leading dash, a space, a control
character — is refused, and the destination is passed after `--` so ssh cannot
read it as one either.

## License

MIT — see [LICENSE](LICENSE).
