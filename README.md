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
herdr plugin install Poor-Plebs/herdr-remote-panes --ref v0.4.18
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

Or take the key. A built-in bound to nothing gives it up, which is how
`prefix+c` — the obvious key for a new tab — can open one on the machine you are
looking at instead:

```toml
[keys]
new_tab = ""
```

Those go directly under `[keys]`, and `[keys]` has to come before the
`[[keys.command]]` blocks. Written after one, a line like `new_tab = ""` becomes
part of that block rather than a setting: TOML reads it as another field of the
binding above it. Nothing complains, because the file is valid — and the
built-in keeps the key.

`herdr config check` will not tell you: a config binding a plugin action to a
key Herdr already owns is reported as `config: ok`. What you see instead is a
key that does the built-in thing, and nothing anywhere saying why. If a binding
seems to do nothing, try another key before looking any further.

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
`~/.ssh/config`, and last a machine you have connected to that is in neither
file — connecting to a name you selected in a terminal makes one of those, and
it stays listed while it is connected, since this is the screen that
disconnects it. Each says how it is reached and how it is doing.

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

**A machine's tabs are tabs here.** Mirrored terminals are placed where the
machine has them: terminals sharing a tab over there share one here, and a tab
opened over there arrives as a tab. That is `placement`, and it defaults to
`follow`.

It did not until v0.4.0, when it defaulted to `split` — the first terminal
opened a tab in the machine's space and every one after it split that tab, so a
machine with three tabs open arrived as one tab here with three panes in it.
That needed a setting to fix, and a mirror that does not mirror the shape is not
something anybody should have to discover a setting for.

What cannot be followed is the arrangement *inside* a tab. Herdr says which tab
a terminal on the machine is in, not how the panes there are divided, so a tab
split left and right over there is a tab with two panes here, in the machine's
order, laid out however Herdr lays them out.

To override it — everything in one tab, or a tab each regardless of the
machine:

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

**A mirror that dies without saying why is read as a tab you closed.** The
bridge behind a mirrored pane records a failure on its way out, which is how a
dropped connection is told from a tab somebody shut — but a process killed
outright records nothing, and neither does one Herdr stops without warning. That
looks exactly like a close, so with `close_propagates` on the terminal goes on
the machine too.

It takes a `kill -9` of a mirror pane's process, or something equivalent, to get
there. The protection against the larger version — every pane vanishing at once,
because Herdr restarted — is that a new daemon does not treat panes it has never
seen as closed, and a Herdr restart takes the daemon with it.

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

Until you pick the machine from the menu again, which forgets what was closed
and brings those terminals back. It is the same key that retries a machine that
was given up on, and it means the same thing: start afresh with this one.

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
seconds, with no restart. It did not until v0.4.0: the daemon read the file
when it started and again only as a side effect of pressing `m`, so every other
setting was fixed for the session while the file plainly said otherwise. If you
set `placement` to `tab` and watched terminals keep arriving as splits, that is
what it was.

A file caught half-written is not read — saving is not atomic in every editor,
and a pass comes round every couple of seconds — so the settings in use stay
the ones that last parsed, and the log says so.

**A slow machine does not hold up the menu.** Machines are asked what they have
open at the same time as each other, and without the lock the daemon answers on
— so opening the menu, or invoking any action, does not wait for a machine that
has stopped answering. It used to wait for all of them, one after another: two
machines taking two seconds each made a status request take 3.7.

Connecting to a machine, or opening a terminal on one, is different: those talk
to the machine while holding the daemon, because that is the thing you asked
for and the answer is what you are waiting for anyway.

**An unreachable machine is left alone** rather than retried forever in the
background. How soon depends on the cause: something that might pass on its own
gets a second try, and something that needs you — a changed host key, a name
that does not resolve, a key the machine will not take — gets none, because the
second try would fail in exactly the same way. Fix whatever is wrong and pick
the machine from the menu, which is how you say "try now".

**Unless every machine goes at once**, which is not about the machines. A lid
closing or a VPN dropping fails all of them in the same pass, so that case
retries itself: 5s, then 15s, 45s, 2m15s, and on up to every 5 minutes for as
long as it takes. They still show as unreachable in the meantime, and you can
still press enter on one to try it now. A machine that fails while the others
are fine has something wrong with *it* and waits for you, as above — and if any
of them failed for a reason that will not clear on its own, none of it retries,
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

When something looks wrong — a machine that will not mirror, a terminal that
will not open, a setting that seems ignored — see
[docs/troubleshooting.md](docs/troubleshooting.md).

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

Building, testing and releasing this plugin are covered in
[docs/development.md](docs/development.md).

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
