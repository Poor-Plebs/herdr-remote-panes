# herdr-remote-panes

A [Herdr](https://herdr.dev) plugin that mirrors panes from remote Herdr servers
into your local session, each one named `<pane>@<host>`.

Run Herdr on a hub machine, point the plugin at the machines you work on, and
the panes running over there show up here — created, renamed and closed in step
with the originals.

```
hub (your laptop)                    workbox (over SSH)
┌───────────────────────────┐        ┌──────────────────────────┐
│ workspace: workbox        │        │ session: remote          │
│  ├── build@workbox    ◄───┼────────┤  ├── build               │
│  ├── tests@workbox    ◄───┼────────┤  ├── tests               │
│  └── claude@workbox   ◄───┼────────┤  └── claude              │
└───────────────────────────┘        └──────────────────────────┘
```

## How it works

Herdr has no inbound pane federation: nothing on the remote machine can push a
pane into your session. So this plugin pulls instead. A daemon on the hub polls
each host over a multiplexed SSH connection, and for every remote pane it opens
a local plugin pane bridged to that pane's terminal.

The bridge is Herdr's own direct terminal attach, so a mirror is a real live
terminal, not a screen scrape.

| Piece | What it does |
| --- | --- |
| `[[startup]]` daemon | Polls each host, reconciles mirrors, names them |
| `herdr pane list` over SSH | Discovers remote panes |
| `plugin.pane.open` | Creates the local mirror pane |
| `herdr terminal attach` | Bridges the remote terminal (interactive) |
| `herdr terminal session observe` | Bridges it read-only (`observe` mode) |
| `pane.rename` | Applies the `<pane>@<host>` name |

## Requirements

- Herdr 0.8.0 or newer on the hub **and** on every remote host
- Passwordless SSH to each host (`ssh workbox` must work on its own)
- Herdr on each host too — the session is started for you over SSH
- Linux or macOS — Herdr's direct terminal attach is Unix-only
- Go 1.22+ on the hub, to build the plugin

## Install

```bash
herdr plugin install Poor-Plebs/herdr-remote-panes
```

Then edit the config and restart Herdr so the daemon picks it up:

```bash
$EDITOR "$(herdr plugin config-dir poorplebs.remote-panes)/config.json"
herdr server stop && herdr
```

## Configuration

`config.json` in the plugin config directory:

```json
{
  "poll_interval": "2s",
  "session": "remote",
  "mode": "attach",
  "placement": "tab",
  "label_format": "{name}@{host}",
  "max_mirrors": 32,
  "hosts": [
    { "target": "workbox" },
    { "target": "ci.example.com", "label": "ci", "mode": "observe" },
    { "target": "devbox", "session": "agents", "placement": "split" }
  ]
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `poll_interval` | `2s` | How often each host is polled. Minimum 500ms |
| `session` | `remote` | Which remote Herdr session to mirror (see below) |
| `mode` | `attach` | `attach` for interactive, `observe` for read-only |
| `placement` | `split` | `tab`, `split`, `zoomed` or `overlay` |
| `label_format` | `{name}@{host}` | Supports `{name}`, `{host}`, `{agent}`, `{pane}` |
| `workspace` | per host | Shared workspace label; default is one per machine |
| `auto_start` | `true` | Start the remote session over SSH when it is not running |
| `herdr_bin` | probed | Remote `herdr` path (see below) |
| `max_mirrors` | `32` | Per-host cap, so a busy remote cannot flood the session |

Per host: `target` (SSH destination), `label` (name suffix, defaults to the
target), `session` (remote `HERDR_SESSION`), `mode`, `placement`, `workspace`
(pin mirrors to a local workspace id), `herdr_bin`, `disabled`.

Mirrors from a host land in a workspace named after it, created on demand.

### Which remote session gets mirrored

By default this plugin mirrors the remote session named **`remote`**, not the
remote's default session. Sessions are fully independent — separate panes,
tabs, workspaces and sockets — so mirroring a dedicated one keeps the hub well
clear of whatever you are already doing on that machine.

With `auto_start` on (the default) the hub starts that session over SSH itself,
so a host only needs the `herdr` binary and working SSH. To drive it by hand
instead, run this on the remote host:

```bash
herdr --session remote
```

Panes you open there show up on the hub. Panes in the remote's default session
do not.

Override it globally with the top-level `session` key, or per host:

```json
{ "target": "devbox", "session": "agents" }
```

To mirror the remote's unnamed default session after all, name it `default`:

```json
{ "target": "devbox", "session": "default" }
```

### `attach` vs `observe`

`attach` runs `herdr terminal attach` on the far side. The mirror is fully
interactive — you type into it and the remote pane responds.

`observe` streams `herdr terminal session observe` and renders it read-only.
It takes no ownership of the remote terminal and does not pin its size, so any
number of machines can watch the same pane at once.

The difference matters because **direct attach is exclusive**: one attached
client per remote terminal. Two hubs mirroring the same terminal in `attach`
mode will fight over it, and an attach also locks that terminal's size for as
long as it lasts. Someone working in the remote machine's own Herdr UI is not
affected — a full UI client is not an attach owner.

## Actions

| Action | Effect |
| --- | --- |
| `poorplebs.remote-panes.connect` | Start mirroring a host (uses the current selection as the target) |
| `poorplebs.remote-panes.open` | Open a new pane on a host; it mirrors back automatically |
| `poorplebs.remote-panes.disconnect` | Stop mirroring a host and close its mirrors |
| `poorplebs.remote-panes.refresh` | Reconcile every host now |
| `poorplebs.remote-panes.status` | Report connected hosts and mirror counts |

Bind one in `config.toml`:

```toml
[[keys.command]]
key = "prefix+r"
type = "plugin_action"
command = "poorplebs.remote-panes.refresh"
description = "refresh remote panes"
```

## Several machines at once

One local Herdr, as many machines as you like, as many panes each. List them
under `hosts`:

```json
{
  "hosts": [
    { "target": "bot" },
    { "target": "prod" },
    { "target": "staging", "mode": "observe" }
  ]
}
```

By default each machine gets its own workspace, named after it:

```
workspace: bot          workspace: prod         workspace: staging
  build@bot               deploy@prod             tail@staging   (read-only)
  claude@bot              psql@prod
```

Set a top-level `workspace` to put every machine in **one** layout instead:

```json
{
  "workspace": "remote",
  "placement": "split",
  "hosts": [{ "target": "bot" }, { "target": "prod" }]
}
```

```
workspace: remote
  build@bot │ claude@bot │ deploy@prod │ psql@prod
```

`workspace` is a label, created when missing, and a host may override it — so
you can group some machines together and keep others apart. Pair it with
`placement`: `split` tiles the mirrors side by side, `tab` gives each its own
tab.

To open a fresh pane on a particular machine, invoke the `open` action with
that host — the pane is created over there and mirrored straight back.

### Finding the remote binary

`ssh host <command>` does not run a login shell, so an install under
`~/.local/bin` — where Herdr's installer puts it for a non-root user — is not on
`PATH` even though an interactive login finds it fine. The plugin probes the
usual locations (`$PATH`, `~/.local/bin`, `/usr/local/bin`, Homebrew, Nix,
mise). Set `herdr_bin` if yours lives somewhere else:

```json
{ "target": "bot", "herdr_bin": "/opt/herdr/bin/herdr" }
```

## Behaviour worth knowing

- **Closing a mirror keeps it closed.** The daemon will not reopen a mirror you
  closed by hand until that remote pane goes away and comes back. This survives
  a restart: the daemon records what it opened under the plugin state directory
  and adopts those panes next time rather than opening a second set.
- **A mirror workspace holds only mirrors.** Herdr opens a shell with every new
  workspace; once a mirror replaces it, that placeholder is closed.
- **A remote pane that closes closes its mirror**, on the next poll.
- **A mirror that fails backs off** — five failed attempts and that terminal is
  left alone. Failures are written to `mirror.log` in the plugin state
  directory and printed into the pane before it closes.
- **Size mismatch is the main rough edge.** A mirror pane and its remote pane
  are rarely the same size; `attach` pins the remote to the mirror's size, and
  `observe` reconnects at the new size when the mirror is resized.

## Development

```bash
git clone https://github.com/Poor-Plebs/herdr-remote-panes
cd herdr-remote-panes
go build -o bin/herdr-remote-panes .
herdr plugin link "$PWD"
go test ./...
```

`herdr plugin link` does not run build commands, so rebuild by hand while
iterating. Logs: `herdr plugin log list --plugin poorplebs.remote-panes`.

## Trust

A Herdr plugin is ordinary code running with your privileges, and this one runs
`ssh` to machines you name. Read the source before installing it — the same
advice Herdr's own [plugin docs](https://herdr.dev/docs/plugins/) give.

## License

MIT — see [LICENSE](LICENSE).
