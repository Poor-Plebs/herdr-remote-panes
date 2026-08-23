# herdr-remote-panes

Work on other machines from one [Herdr](https://herdr.dev), without leaving it.

Press a key, pick a machine, and you have a terminal on it — in its own space,
named after it. Machines come from your `~/.ssh/config`, so there is usually
nothing to configure.

```
  ☁  workbox          ← a machine you are working on
     shell@workbox
  ☁  ci
     shell@ci
  ~                   ← your own local space
```

## Install

```bash
herdr plugin install Poor-Plebs/herdr-remote-panes
```

You need Go on this machine to build it, SSH access to the machines, and Linux
or macOS. Nothing needs installing on the far side.

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

```
  Connect to a machine

 > 1. workbox                  connected · ssh
   2. ci                       from ~/.ssh/config · ssh
   3. buildbox                 unreachable · enter to retry

  ↑↓ jk move · pgup/pgdn g/G jump · 1-9 pick · enter connect
  m toggle mirroring (experimental) · q cancel
```

`enter` connects and gives you a terminal. `m` turns mirroring on for a machine
(see below). The list scrolls, so a long SSH config is fine.

## Everyday use

| You want | Do this |
| --- | --- |
| A terminal on a machine | Open the menu, pick it |
| Another terminal on it | Open a tab while in its space |
| That machine's space back | Open the menu, pick it again |
| To see what is connected | Run the `status` action |

Opening a tab inside a machine's space gives you one **on that machine**. Herdr's
own new-tab key and the plus icon always open a local shell and cannot be
intercepted by a plugin, so such a pane is moved onto the machine a moment
after. To skip that round trip, bind the new-tab key to the plugin instead:

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

**Machines are marked.** A machine's space is `☁  workbox`, and `⚠  workbox`
while it cannot be reached.

**A dropped connection comes back.** A terminal whose SSH link fails is
reopened; one you close stays closed. Restarting Herdr restores the machines you
had connected.

**An unreachable machine is left alone after two tries**, rather than retried
forever. Picking it from the menu is an explicit "try now".

**Machines without Herdr just work.** They get a plain SSH terminal; mirroring
is the only part that needs Herdr on both ends.

**Terminals you open on a machine stay on it.** Disconnecting closes the panes
here, not the work there — a build running in one keeps running. With mirroring,
that also means the space this creates on the machine outlives the session, and
the terminals in it are still there next time. That is what makes reconnecting
pick up where you left off, and it also means they accumulate if you never look:

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
| `hosts[].label` | the target | How it is named here |
| `hosts[].disabled` | `false` | Skip it without removing it |
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

**A machine's space is empty or missing.** With mirroring on, it usually means
the machine has nothing running:

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

**A machine says `unreachable, not retrying`.** Two failed attempts and it
stops, rather than reconnecting forever in the background. Fix the cause, then
pick it from the menu again to retry. The most common cause is a changed host
key:

```
REMOTE HOST IDENTIFICATION HAS CHANGED
```

That means the machine now presents a different key than the one recorded in
`~/.ssh/known_hosts`. A rebuilt or reinstalled machine does this, and so does
someone sitting between you and it. Check the fingerprint against the machine
itself before removing the old entry — the whole point of the warning is that
you cannot tell the two cases apart from here.

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

**A keybinding does nothing.** First check the config itself is being read:

```bash
herdr config check
```

If that is happy, the binding probably clashes with a built-in, which wins
silently. Taken:
`b c e g h j k l n o p q r s v w x z tab minus alt+g shift+d shift+g shift+n
shift+p shift+r shift+t shift+w shift+x shift+tab`.

## How it works

Herdr cannot push panes between machines, so this pulls: a daemon polls each
machine over one reused SSH connection and opens a local pane for what it finds.
A mirrored terminal is bridged with Herdr's own direct terminal attach, so it is
a real live terminal rather than a copy of the screen.

Requires Herdr 0.8.0+ and Go 1.25+.

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
