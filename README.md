# lifeo

A personal, local tracker for goals, plans and time. One binary, one SQLite
file, a terminal UI for you and a plain CLI with JSON output for scripts and
coding agents.

Goals live at three levels, year, quarter and month, and a goal is either
numeric with a hand-updated running total, or yes/no. At the end of a period
you mark each one hit or missed and set the next batch. Parent links are
optional. More parts, such as tasks and time planning, are on the way.

## Install

```sh
go install github.com/thisisnic/lifeo/cmd/lifeo@latest
```

Or from a checkout: `make build` puts `./lifeo` in the repo.

## Use

```sh
lifeo                                  # open the terminal UI
lifeo goal add "run 500 km" --period 2026 --target 500 --unit km --why "..."
lifeo goal add "finish the garden" --period 2026-Q3 --parent 1
lifeo goal progress 1 120 --note "end of March"
lifeo goal mark 2 hit
lifeo goal list --json                 # for scripts and agents
```

In the UI: `a` add, `e` edit, `p` record progress, `h` hit, `m` missed,
`c` clear, `d` delete, `x` private mode, `j`/`k` move, `q` quit.

The database lives at `~/.local/share/lifeo/lifeo.db` (or under
`$XDG_DATA_HOME`). Point elsewhere with `--db` or `LIFEO_DB`.

## Private mode

Somewhere you'd rather not have people read over your shoulder, start with
`lifeo --private` or set `LIFEO_PRIVATE=1`, or press `x` in the UI. It hides
the why, every amount and history notes. Percentages stay. Goal statements
are your own words and are shown as written, so keep figures out of them if
that matters.

## Backups

A backup is an encrypted copy of the database, written as one file,
`lifeo.db.age`, into a folder you choose, typically a private git repo of
your own. Each backup replaces the last; the repo's history is the history.
Set up once:

```sh
lifeo key new
```

That writes an age private key to `~/.config/lifeo/key.txt` and prints the
public key with a config file to copy into `~/.config/lifeo/config.toml`:

```toml
[backup]
dir = "~/lifeo-data"          # your private data repo
recipient = "age1..."         # public key, encrypts
identity_file = "~/.config/lifeo/key.txt"  # private key, decrypts
on_quit = true                # back up every time the UI exits
git = true                    # then git commit and push from dir
```

Keep a copy of the private key in a password manager. Without it the
backups cannot be opened. Then:

```sh
lifeo backup                  # back up now (skipped if unchanged)
lifeo restore                 # put the backup in place of the database
```

With `git = true` lifeo commits and pushes the file for you after each
backup, so the data repo needs an upstream and credentials that work without
a prompt, such as an SSH key. A failed push is reported and retried next
time. Without it, commit and push the data repo however you like.

## Development

```sh
make test
make build
```

Go 1.26, Cobra, Bubble Tea v2, huh, modernc SQLite, age. Every commit is
reviewed by [roborev](https://github.com/kenn-io/roborev).
