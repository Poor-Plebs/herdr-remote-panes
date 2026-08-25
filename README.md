# herdr-remote-panes

[![CI](https://github.com/Poor-Plebs/herdr-remote-panes/actions/workflows/ci.yml/badge.svg)](https://github.com/Poor-Plebs/herdr-remote-panes/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/Poor-Plebs/herdr-remote-panes?logo=go&label=Go)](go.mod)
[![License](https://img.shields.io/github/license/Poor-Plebs/herdr-remote-panes)](LICENSE)

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
the cursor. The list scrolls, so a long SSH config is fine.

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

**An unreachable machine is left alone** rather than retried forever in the
background. How soon depends on the cause: something that might pass on its own
gets a second try, and something that needs you — a changed host key, a name
that does not resolve, a key the machine will not take — gets none, because the
second try would fail in exactly the same way. Fix whatever is wrong and pick
the machine from the menu, which is how you say "try now".

**Machines without Herdr just work.** They get a plain SSH terminal. Mirroring
is the only part that needs Herdr at both ends, and a machine that turns out not
to have it falls back rather than refusing to connect.

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
| `hosts[].mode` | `ssh` | `ssh` plain terminal; `attach` or `observe` to mirror |
| `hosts[].label` | the target | How it is named here. Must differ from every other machine's: it names the machine's space, and two machines sharing one would take each other's terminals |
| `hosts[].disabled` | `false` | Skip it without removing it |
| `hosts[].session` | the global one | Which Herdr session on *this* machine |
| `hosts[].placement` | the global one | How *this* machine's terminals are placed |
| `hosts[].workspace` | the global one | Which space *this* machine's terminals land in |
| `hosts[].herdr_bin` | found automatically | Where `herdr` lives on *this* machine |
| `mode` | `ssh` | Default mode for machines that do not set one |
| `placement` | `split` | How terminals are placed here: `split`, `tab`, `zoomed` |
| `workspace_format` | `☁  {host}` | How a machine's space is named |
| `workspace_format_down` | `⚠  {host}` | How it is named while unreachable |
| `workspace` | one per machine | Put every machine in one space instead |
| `remote_workspace_format` | `☁  {hub}` | How *this* machine's space is named **on** the machine, so it is recognisable from that end |
| `label_format` | `{name}@{host}` | How its terminals are named. `{name}` `{host}` `{agent}` `{pane}` |
| `poll_interval` | `2s` | How often machines are checked |
| `close_propagates` | `true` | Closing a mirrored tab closes it on the machine |
| `capture_new_panes` | `true` | Move a local pane opened in a machine's space onto it |
| `auto_start` | `true` | Start Herdr on the machine when mirroring needs it |
| `scope` | `shared` | `shared` mirrors the shared space; `all` mirrors everything |
| `session` | `default` | Which Herdr session on the machine is shared |
| `max_mirrors` | `32` | Most terminals to mirror per machine |
| `takeover` | `true` | Take over a stale connection left by a closed terminal |
| `herdr_bin` | found automatically | Where `herdr` lives on the machine |

## When something looks wrong

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

The most common of them is a changed host key:

```
REMOTE HOST IDENTIFICATION HAS CHANGED
```

That means the machine now presents a different key than the one recorded in
`~/.ssh/known_hosts`. A rebuilt or reinstalled machine does this, and so does
someone sitting between you and it. Check the fingerprint against the machine
itself before removing the old entry — the whole point of the warning is that
you cannot tell the two cases apart from here.

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
panes. It reads `not running` when nothing answers, which is itself an answer.

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
its terminal dying rather than by being asked. Connecting does talk to it, once,
to check it answers: without that an unreachable machine would report `ok` and
the trouble would only show up later as terminals that will not stay open. Only
a mirrored machine is polled over SSH, for the list of terminals to keep in
step, over one reused connection.

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
that survives is a change no test would have caught. Most survivors are
equivalent or unreachable, which is worth knowing too; the ones that are
neither are where the bugs have been. Only covered lines are tried, since a
change to a line nothing runs survives by definition.

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
