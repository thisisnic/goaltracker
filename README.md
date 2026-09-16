# goaltracker

A personal, local tracker for goals, plans and time. One binary, one SQLite
file, a terminal UI for you and a plain CLI with JSON output for scripts and
coding agents.

Goals live at three levels, year, quarter and month, and a goal is either
numeric with a hand-updated running total, or yes/no. At the end of a period
you mark each one hit or missed and set the next batch. Parent links are
optional. More parts, such as tasks and time planning, are on the way.

## Install

```sh
go install github.com/thisisnic/goaltracker/cmd/goaltracker@latest
```

Or from a checkout: `make build` puts `./goaltracker` in the repo.

## Use

```sh
goaltracker                                  # open the terminal UI
goaltracker goal add "run 500 km" --period 2026 --target 500 --unit km --why "..."
goaltracker goal add "finish the garden" --period 2026-Q3 --parent 1
goaltracker goal progress 1 120 --note "end of March"
goaltracker goal mark 2 hit
goaltracker goal list --json                 # for scripts and agents
```

In the UI: `a` add, `e` edit, `p` record progress, `h` hit, `m` missed,
`c` clear, `d` delete, `x` private mode, `j`/`k` move, `q` quit.

The database lives at `~/.local/share/goaltracker/goaltracker.db` (or under
`$XDG_DATA_HOME`). Point elsewhere with `--db` or `GOALTRACKER_DB`.

## Private mode

Somewhere you'd rather not have people read over your shoulder, start with
`goaltracker --private` or set `GOALTRACKER_PRIVATE=1`, or press `x` in the UI. It hides
the why, every amount and history notes. Percentages stay. Goal statements
are your own words and are shown as written, so keep figures out of them if
that matters.

## Backups

A backup is an encrypted copy of the database, written as one file,
`goaltracker.db.age`, into a folder you choose, typically a private git repo of
your own. Each backup replaces the last; the repo's history is the history.
Set up once:

```sh
goaltracker key new
```

That writes an age private key to `~/.config/goaltracker/key.txt` and prints the
public key with a config file to copy into `~/.config/goaltracker/config.toml`:

```toml
[backup]
dir = "~/goaltracker-data"          # your private data repo
recipient = "age1..."         # public key, encrypts
identity_file = "~/.config/goaltracker/key.txt"  # private key, decrypts
on_quit = true                # back up every time the UI exits
git = false                   # true: git commit and push from dir too
```

Keep a copy of the private key in a password manager. Without it the
backups cannot be opened. Then:

```sh
goaltracker backup                  # back up now (skipped if unchanged)
goaltracker restore                 # put the backup in place of the database
```

With `git = true` goaltracker commits and pushes the file for you after each
backup, so the data repo needs an upstream and credentials that work without
a prompt, such as an SSH key. A failed push is reported and retried next
time. Without it, commit and push the data repo however you like.

## Upgrading from lifeo

The command used to be called `lifeo`. Nothing moves your data across on
its own. Do this once, with neither lifeo nor goaltracker running, and
before goaltracker has created anything of its own. If you have already
run `goaltracker` or `goaltracker key new`, remove what it made first:
`~/.local/share/goaltracker` and `~/.config/goaltracker` are safe to delete
at that point, as they hold only an empty database or a key you have not
used.

```sh
mv ~/.config/lifeo ~/.config/goaltracker
rm -f ~/.config/goaltracker/last-backup-*
sed -i 's|/.config/lifeo/|/.config/goaltracker/|' ~/.config/goaltracker/config.toml
mkdir -p ~/.local/share/goaltracker
# the database and, if lifeo last exited uncleanly, its -wal and -shm files
for f in ~/.local/share/lifeo/lifeo.db*; do
  mv "$f" ~/.local/share/goaltracker/goaltracker.db"${f#*/lifeo.db}"
done
rmdir ~/.local/share/lifeo
cd <your backup folder> && git mv lifeo.db.age goaltracker.db.age && git commit -m "rename" && git push
```

`LIFEO_DB` and `LIFEO_PRIVATE` are now `GOALTRACKER_DB` and
`GOALTRACKER_PRIVATE`.

## Development

```sh
make test
make build
```

Go 1.26, Cobra, Bubble Tea v2, huh, modernc SQLite, age. Every commit is
reviewed by [roborev](https://github.com/kenn-io/roborev).
