# goaltracker

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/thisisnic/goaltracker)](https://github.com/thisisnic/goaltracker/releases)
[![ci](https://github.com/thisisnic/goaltracker/actions/workflows/ci.yml/badge.svg)](https://github.com/thisisnic/goaltracker/actions/workflows/ci.yml)

**[Releases](https://github.com/thisisnic/goaltracker/releases)** | **[Quick Start](#quick-start)** | **[Backups](#backups)**

A personal goal tracker that lives in your terminal and keeps your data on
your machine. Set goals for the year, the quarter and the month, record
progress as you go, mark them hit or missed, and keep an encrypted backup in
a git repo you own. One binary, one SQLite file, no account.

```text
goaltracker · goals
╭──────────────────────────────────────────────────╮╭──────────────────────────────────────────╮
│ 2026                                             ││ run 500 km                               │
│   2026     run 500 km                        43% ││ period 2026 (year)   id #1               │
│   ✓ 2026-Q2  run a half marathon                 ││                                          │
│   2026     finish the garden                     ││ why                                      │
│   ✓ 2026-04  plant the hedge                     ││ Be able to say yes to a long day out     │
│ 2027                                             ││ without thinking about it.               │
│   2027     swim 5 km in open water               ││                                          │
│                                                  ││ progress 215 km / 500 km                 │
│                                                  ││ █████████████████░░░░░░░░░░░░░░░░░░░░░░░ │
│                                                  ││                                          │
│                                                  ││ history                                  │
│                                                  ││   2026-03-31  120 km  end of March       │
│                                                  ││   2026-05-31  215 km  end of May         │
│                                                  ││                                          │
╰──────────────────────────────────────────────────╯╰──────────────────────────────────────────╯

```

## How It Works

1. Add goals for a period: `2026`, `2026-Q2` or `2026-04`. The level follows
   from the period. A goal is numeric, with a target and a running total you
   update by hand, or yes/no.
2. Hang quarterly goals off yearly ones and monthly off quarterly, or don't.
   Parent links are optional and loose.
3. Record progress when you feel like it, mark each goal hit or missed at the
   end of its period, and set the next batch.

Every update to a running total is kept with its date, so the history is
there when you want to look back.

## Quick Start

```bash
goaltracker                      # open the terminal UI
```

In the UI: `a` add, `e` edit, `p` record progress, `h` hit, `m` missed,
`c` clear, `d` delete, `x` private mode, `j`/`k` move, `q` quit.

The same data is there on the command line, with `--json` for scripts and
coding agents:

```bash
goaltracker goal add "run 500 km" --period 2026 --target 500 --unit km --why "..."
goaltracker goal add "run a half marathon" --period 2026-Q2 --parent 1
goaltracker goal progress 1 215 --note "end of May"
goaltracker goal mark 2 hit
goaltracker goal list --json
```

## Features

- **Three levels, one tree** - Yearly, quarterly and monthly goals, shown as
  a tree with progress at a glance.
- **Numeric or yes/no** - A target with a unit, or a simple done or not.
  Units are placed sensibly: `£35,000`, `215 km`, `50%`.
- **Stretch targets** - A numeric goal can carry a stretch figure beyond its
  target. It stays out of the way until the target is reached, then the
  goal's page shows it and the bar carries on towards it.
- **Progress history** - Every running-total update is kept and dated.
- **Private mode** - Hide the why, every amount and every note when someone
  might be reading over your shoulder. Press `x`, start with `--private`, or
  set `GOALTRACKER_PRIVATE=1`.
- **Encrypted backups** - One age-encrypted file, written to a folder you
  choose, and optionally committed and pushed to your own private git repo
  every time the UI exits.
- **Agent friendly** - A plain CLI with JSON output, so a coding agent can
  read and update your goals without touching the UI.
- **Local and portable** - A single static binary and a single SQLite file
  at `~/.local/share/goaltracker/goaltracker.db` (or under `$XDG_DATA_HOME`).
  Point elsewhere with `--db` or `GOALTRACKER_DB`.
- **Self-updating** - `goaltracker update` fetches the latest release,
  verifies it against the published checksums, and swaps the binary in place.

## Private Mode

Somewhere you'd rather not have people read over your shoulder, start with
`goaltracker --private`, set `GOALTRACKER_PRIVATE=1`, or press `x` in the
UI. The why, amounts and history notes are hidden; percentages stay. Goal
statements are your own words and are shown as written, so keep figures out
of them if that matters. Editing is disabled while private, since the form
would show the hidden values.

## Backups

A backup is an encrypted copy of the database written as one file,
`goaltracker.db.age`, into a folder you choose. Make that folder a private
git repo and the repo's history is the backup history. Set up once:

```bash
goaltracker key new
```

That writes an age private key to `~/.config/goaltracker/key.txt` and prints
your public key together with a config to copy into
`~/.config/goaltracker/config.toml`:

```toml
[backup]
dir = "~/goaltracker-data"                      # your private data repo
recipient = "age1..."                           # public key, encrypts
identity_file = "~/.config/goaltracker/key.txt" # private key, decrypts
on_quit = true                                  # back up every time the UI exits
git = false                                     # true: git commit and push from dir too
```

Keep a copy of the private key in a password manager. Without it the backups
cannot be opened. Then:

```bash
goaltracker backup            # back up now (skipped if unchanged)
goaltracker restore           # put the backup in place of the database
```

With `git = true` goaltracker commits and pushes the file after each backup.
The data repo needs a remote and credentials that work without a prompt, such
as an SSH key in an agent. A failed push is reported and retried next time.

## Installation

**Binary (macOS / Linux / Windows):** download the archive for your platform
from [GitHub Releases](https://github.com/thisisnic/goaltracker/releases),
check it against `checksums.txt`, and put `goaltracker` on your `PATH`.

**With Go:**

```bash
go install github.com/thisisnic/goaltracker/cmd/goaltracker@latest
```

**Updating:**

```bash
goaltracker version           # what you have
goaltracker update --check    # is there a newer release?
goaltracker update            # install it
```

## Commands

| Command | What it does |
| --- | --- |
| `goaltracker` | Open the terminal UI; `--private` |
| `goaltracker goal add STATEMENT --period P` | Add a goal; `--target`, `--stretch`, `--unit`, `--why`, `--parent`, `--json` |
| `goaltracker goal list` | Show the goal tree; `--year`, `--level`, `--json` |
| `goaltracker goal show ID` | One goal with its why and history; `--json` |
| `goaltracker goal edit ID` | Change statement, why, target, stretch, unit or parent; `--no-parent` |
| `goaltracker goal progress ID VALUE` | Record a new running total; `--note` |
| `goaltracker goal mark ID hit\|missed\|clear` | Set the outcome at the end of a period |
| `goaltracker goal delete ID` | Delete a goal and its history; `--yes` |
| `goaltracker key new` | Create the backup keypair and print the config; `--out` |
| `goaltracker backup` | Write an encrypted backup; `--dir`, `--recipient` |
| `goaltracker restore [FILE]` | Replace the database with a backup; `--identity`, `--yes` |
| `goaltracker update` | Install the latest release; `--check`, `--force` |
| `goaltracker version` | Print the version |

Every command also takes `--db` and `--config` to point at a different database or config file.

## Development

```bash
make test
make build
```

Go 1.26, Cobra, Bubble Tea v2, huh, modernc SQLite, age. Every commit is
reviewed by [roborev](https://github.com/kenn-io/roborev). Releases are cut
by tagging: `git tag -a v0.2.0 -m "..." && git push origin v0.2.0`.

## License

[MIT](LICENSE)
