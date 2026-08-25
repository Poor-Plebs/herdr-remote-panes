# Pairing

Status: **planned, nothing built.** This is the plan of record; it is written
down so that decisions already taken are not re-litigated.

## What it is

Two people on two machines share one Herdr space on a virtual machine they both
reach over SSH — same user there, different keys. Each sees the other's
terminals in their own sidebar, live.

## The model

Each person **drives the terminals they created and watches everyone else's.**

That is the whole idea, and it is what makes pairing fit Herdr rather than fight
it. Herdr allows one controller per terminal and unlimited observers, so two
people attaching to one terminal is a takeover war by construction. Splitting
ownership per terminal means that never comes up: two drivers, disjoint sets.

For the times you do need to touch a terminal you don't drive, you push into it
instead of taking it:

    herdr pane run <pane-id> <command>
    herdr pane send-text <pane-id> <text>
    herdr pane send-keys <pane-id> <key>...

None of these require an attachment. They inject into a pane by id whoever is
driving it, which is exactly the escape hatch pairing needs and the reason this
design does not need a handover protocol.

## Decisions taken

- **A terminal nobody here created is watched, not driven.** That covers your
  colleague's terminals, terminals made by using Herdr on the machine directly,
  and anything predating pairing. Watching never surprises the other person;
  driving by default would.
- **"Take this one over" is offered in the menu.** Sending commands is the
  requirement; deliberate takeover is the release valve when the other person
  has gone home with a terminal still theirs.
- **Pairing is switched on and off by itself**, per machine. See below.

## The switch

Pairing is a fourth mode, alongside `ssh`, `attach` and `observe`:

    mode: "pair"

rather than a separate flag. Two reasons. It is genuinely a mode — "attach to
mine, observe theirs" sits exactly beside "attach to everything" and "observe
everything", and a `pairing: true` orthogonal to `mode: attach` would have no
sensible meaning. And it inherits everything modes already have: per-host
override, the picker menu, the status line, config validation.

Containment matters here, so, concretely:

- A machine not in `pair` mode behaves exactly as it does today. Ownership is
  never consulted; `ModeFor` returns what it returns now.
- Unknown modes already fall back to `ssh`. A config saying `mode: "pair"` read
  by an older build degrades to plain SSH panes rather than failing.
- The extra actions (send-to-pane, take over) are offered only for machines in
  `pair` mode.

## What it needs

**1. Mode resolved per terminal, not per machine.** The plumbing already carries
mode per pane — each mirror gets its own `HRP_MODE`. Only the decision is
per-machine today.

**2. A record of which terminals are ours.** The plugin creates remote terminals
itself and gets the terminal id back, so it knows at creation time; it needs to
keep that list, persisted beside `Mirrors` so it survives a restart.

Worth stating because it is easy to over-build: **neither side needs to know the
other's identity.** Each side needs to know which terminals are its own, and
everything else is "not mine, so watch it". There is no ownership handshake, no
shared registry, and nothing to agree on between the two machines.

The alternative — marking ownership on the remote pane with
`herdr pane report-metadata` — does not work. It can tag a pane with a title,
token or state label, but the pane listing does not return any of them, so the
plugin cannot read ownership back. Display-only.

**3. An action to send a command to a terminal you don't drive**, and one to
take a terminal over. Both are single CLI calls through the connection that is
already open.

## Sizing

1 and 2 together are the core, and are small: a persisted set of terminal ids
and one decision moved from per-machine to per-terminal. 3 is a few lines each.
Showing whose is whose in the sidebar is polish and can come last.

## Known sharp edges

- **Two spaces answering to one name.** Both of you set the same
  `remote_workspace_format`; if the space is created twice, you are each in a
  different space of the same name, seeing none of each other's terminals, and
  neither of you is wrong. It cannot be prevented from one side, but it is no
  longer silent: it is reported in `status` and the log.
- Creating the shared space is find-then-create with no locking, which is how
  the above happens.
- A terminal whose owner has disconnected stays owned. That is what the menu
  takeover is for.
