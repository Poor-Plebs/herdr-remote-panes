# herdr-remote-panes

Work on several machines from one [Herdr](https://herdr.dev).

Point it at the machines you use. Their terminals show up in your local Herdr,
grouped per machine and named `<pane>@<host>`, and you type in them as if they
were local. Agents running over there appear in your sidebar with the right
name and status.

```
your laptop                          workbox (over SSH)
┌───────────────────────────┐        ┌──────────────────────────┐
│ ☁ workbox                 │        │  build                   │
│   build@workbox       ◄───┼────────┤  tests                   │
│   tests@workbox       ◄───┼────────┤  claude                  │
│   claude@workbox      ◄───┤        │                          │
└───────────────────────────┘        └──────────────────────────┘
```

## Install

```bash
herdr plugin install Poor-Plebs/herdr-remote-panes
```

Then list your machines:

```bash
$EDITOR "$(herdr plugin config-dir poorplebs.remote-panes)/config.json"
```

```json
{
  "hosts": [
    { "target": "workbox" },
    { "target": "ci.example.com", "label": "ci" }
  ]
}
```

`target` is whatever you type after `ssh`. Restart Herdr and the spaces appear.

You need: Go on this machine (to build the plugin), and SSH access to each
machine. That is all — see [Machines without Herdr](#machines-without-herdr).

## Everyday use

| You want | Do this |
| --- | --- |
| Connect to a machine | Open the menu and pick one |
| See a machine's terminals | They appear on their own, in a space named after the machine |
| A new terminal on a machine | Run the `open` action while in that machine's space |
| Bring a space back after closing it | Run the `connect` action, or pick it from the menu |
| Check what is connected | Run the `status` action |

### The machine menu

The `menu` action opens a popup listing every machine from your `~/.ssh/config`
and from this plugin's config, showing which are connected. Move with the arrow
keys or `j`/`k`, jump with `1`-`9`, `enter` to connect, `q` to cancel. Machines
picked from the menu do not have to be in `config.json` first.

Actions are easier on a key. Add to `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+shift+s"
type = "plugin_action"
command = "poorplebs.remote-panes.menu"
description = "connect to a machine"

[[keys.command]]
key = "prefix+shift+c"
type = "plugin_action"
command = "poorplebs.remote-panes.open"
description = "new terminal on this machine"

[[keys.command]]
key = "prefix+shift+m"
type = "plugin_action"
command = "poorplebs.remote-panes.connect"
description = "reconnect remote spaces"
```

Pick keys Herdr does not already use — it will not warn you about a clash, the
built-in binding simply wins. `prefix+shift+r`, for instance, is already
`reload_config`.

The `open` action is safe to bind over your usual new-pane key: in a machine's
space it opens a terminal there, anywhere else it opens a normal local one.

## Things worth knowing

**Closing a space closes those terminals here, not on the machine.** Your work
keeps running over there. Run `connect` to bring the space back.

**Closing one terminal keeps it closed.** It will not reappear behind your back
until that terminal goes away on the machine and comes back.

**Terminals come and go on their own.** Open one on the machine and it appears
here; close it there and it disappears here.

## Machines without Herdr

Herdr on the far side is optional. Without it you get a plain SSH terminal in
that machine's space — run an agent in it like any other terminal. This is
detected automatically; force it with `"mode": "ssh"`.

The difference is that nothing syncs for such a machine: terminals exist
because you opened them. Terminals appearing and disappearing on their own is
the part that needs Herdr on both ends.

## Which session gets mirrored

On each machine, the plugin uses a Herdr session called `remote`, started for
you over SSH, so it never disturbs whatever you already have running there. To
watch it on the machine itself:

```bash
ssh workbox
herdr --session remote
```

You will see the same terminals. Plain `herdr` there shows that machine's own
default session instead, which is not what gets mirrored.

## Settings

All optional, in `config.json`:

| Setting | Default | What it does |
| --- | --- | --- |
| `hosts` | – | The machines. Each takes `target`, and optionally `label`, `mode`, `session`, `workspace`, `placement`, `herdr_bin`, `disabled` |
| `mode` | `attach` | `attach` to type in them, `observe` to only watch, `ssh` for a plain SSH terminal |
| `workspace` | one per machine | Put every machine in one space instead |
| `workspace_format` | `☁  {host}` | How a machine's space is named |
| `label_format` | `{name}@{host}` | How its terminals are named |
| `placement` | `split` | `split`, `tab` or `zoomed` |
| `poll_interval` | `2s` | How often machines are checked |
| `max_mirrors` | `32` | Most terminals to show per machine |
| `session` | `remote` | Which Herdr session to mirror |
| `auto_start` | `true` | Start that session over SSH when it is not running |
| `takeover` | `true` | Take over a stale connection left by a closed terminal |
| `herdr_bin` | found automatically | Where `herdr` lives on the machine |

### Everything in one space

```json
{
  "workspace": "remote",
  "placement": "split",
  "hosts": [{ "target": "workbox" }, { "target": "ci" }]
}
```

### A green or red cloud on remote spaces

Spaces are named `☁  workbox` by default. For a cloud that turns green while
the machine is reachable and red when it is not, add this to
`~/.config/herdr/config.toml` and set `"workspace_format": "{host}"`:

```toml
[ui.sidebar.spaces]
rows = [
  [ { token = "$remote_up", fg = "#3fb950" }, { token = "$remote_down", fg = "#f85149" }, "state_icon", "workspace" ],
  [ "branch", "git_status" ],
]
```

### Watching instead of typing

`"mode": "observe"` shows a machine's terminals read-only. Any number of people
can watch the same terminal that way. `attach` is exclusive — two machines
mirroring the same terminal in `attach` mode will fight over it.

## When something looks wrong

**A space is empty or missing.** Usually the machine has no terminals to show.
Check with:

```bash
ssh workbox 'HERDR_SESSION=remote herdr pane list'
```

**A terminal will not open.** Failures are written to `mirror.log` in the
plugin's state directory, and printed into the pane before it closes:

```bash
herdr plugin log list --plugin poorplebs.remote-panes
cat ~/.local/state/herdr/plugins/poorplebs.remote-panes/mirror.log
```

**A keybinding does nothing.** It probably clashes with a built-in one. These
are taken: `b c e g h j k l n o p q r s v w x z tab minus alt+g shift+d shift+g
shift+n shift+p shift+r shift+t shift+w shift+x shift+tab`.

## How it works

Herdr cannot push panes between machines, so this pulls instead: a small daemon
polls each machine over one reused SSH connection, and for every terminal it
finds it opens a local pane bridged to that terminal with Herdr's own direct
terminal attach. A mirrored terminal is a real live terminal, not a copy of the
screen.

Requires Herdr 0.8.0+, Go 1.22+ on this machine, and Linux or macOS — Herdr's
direct terminal attach is Unix-only.

## Trust

This runs with your privileges and connects by SSH to machines you name. Read
the source before installing it, the same as any Herdr plugin.

## License

MIT — see [LICENSE](LICENSE).
