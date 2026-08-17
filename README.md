# histdb

> Shell history in SQLite.

Having a queryable shell history is awesome. `histdb` records your shell commands in
a SQLite database, so you can query instead of grepping a flat history file.

## What about atuin?

atuin is great if you want a full-featured history manager with syncing,
encryption, TUI search, stats, and newer AI-assisted workflows.

`histdb` is deliberately smaller and simpler. It records shell history in a local
SQLite database and gives you direct, queryable access to it. No sync service, no
account, no background server, no AI features, and no attempt to replace your
shell. Just structured history that stays easy to query, script against, and
analyze. Your regular shell history continues to work too, so if you ever decide
to stop using `histdb` you can.

## What shells are supported

Zsh and Bash today, with other shells coming.

- [x] Zsh
- [x] Bash
- [ ] Fish

## Install

```sh
go install github.com/mattmc3/histdb@latest
```

Or build from source:

```sh
git clone https://github.com/mattmc3/histdb $HOME/src/histdb
cd $HOME/src/histdb
just build      # -> bin/histdb
just install    # -> $GOBIN
```

Enable in Zsh by adding this to `.zshrc`:

```zsh
source <(histdb init zsh)
```

Or in Bash, by adding this to `.bashrc`:

```bash
eval "$(histdb init bash)"
```

Needs bash 5.0 or newer, so on macOS `brew install bash` rather than the 3.2
that ships as `/bin/bash`.

Load it after anything else that sets `PROMPT_COMMAND`, so nothing overwrites
the hooks, but before anything that sets a `DEBUG` trap, which histdb will not
take away from you. If both apply, load [bash-preexec] first and histdb will
hook onto it instead, which settles the trap either way.

[bash-preexec]: https://github.com/rcaloras/bash-preexec

## Usage

Bare `histdb` lists the last 20 commands, oldest first. Matching is always an
explicit `--like` pattern.

```sh
histdb                      # recent commands
histdb --like '%git%'       # commands containing "git"
histdb --like 'git%'        # commands starting with "git"
histdb -d                   # only this directory
histdb -r                   # this directory and the rest of its repository
histdb --since yesterday    # since yesterday midnight
histdb --until "last week"  # up to a week ago
histdb -s                   # only this shell session
histdb -S                   # every shell session
histdb -H                   # oldest matches instead of newest
histdb -n 100               # more rows
histdb --no-dups            # only the newest run of each command
```

Long forms: `--here`, `--repo`, `--session`, `--all-sessions`, `--head`,
`--limit`. Outside a checkout, `-r` behaves like `-d`.

With neither `-s` nor `-S`, the scope is whatever `SHARE_HISTORY` says, since
the zsh wrapper passes `--session` for you when it is off. `-S` overrides that,
so it is how you reach other sessions under `NO_SHARE_HISTORY`. Bash has no
such option, so it searches every session until you ask for `-s`.

| variable         | meaning                                                                             |
| ---------------- | ----------------------------------------------------------------------------------- |
| `HISTDB_FILE`    | database path, exported by `histdb init`, default `$XDG_DATA_HOME/histdb/histdb.db` |
| `HISTDB_SESSION` | session key, set by the shell integration                                           |
| `HISTDB_BIN`     | binary the hooks call, pinned by `histdb init`                                      |

## Ranking by frequency

`-F` collapses runs into one row per command, most run first. Since a command
that ran many times has no single time, directory or exit status, the listing
shows run count and last use instead.

```sh
histdb -F                     # most run commands
histdb -F --like 'git%'       # most run, starting with "git"
histdb -F --prefer-here       # rank this directory's commands first
histdb -F -d                  # only this directory
```

## Time ranges

`--since` is inclusive, `--until` is not. Both take the same vocabulary:

| form                     | example                                                     |
| ------------------------ | ----------------------------------------------------------- |
| a date                   | `2026-01-15`                                                |
| a date and time          | `"2026-01-15 09:30"`, `2026-01-15T09:30`, RFC3339           |
| a keyword                | `now`, `today`, `yesterday`                                 |
| a duration back from now | `2h`, `90m`, `45s`                                          |
| a count of units back    | `"3 days ago"`, `3.days.ago`, `"2 weeks ago"`, `last month` |
| counts that add up       | `"1 year 2 months ago"`, `"3 days 2 hours ago"`             |
| a weekday                | `friday`, `fri`, `"last friday"`                            |
| a month and day          | `"december 25th"`, `"dec 25"`, `"3 march"`                  |
| a time of day            | `10am`, `5pm`, `9:15`, `17:30`, `noon`, `midnight`          |
| unix epoch               | `@1700000000`                                               |

Every relative form means the most recent one at or before now, which is where
history is. `friday` is the Friday just gone, today included if today is a
Friday, where `"last friday"` always goes back a further week. `"dec 25"` in
January is last December. `5pm` is today's, or yesterday's when 5pm has not
come round yet.

The vocabulary is English only. Anything it cannot read is an error listing
what it takes, rather than a silent fall back to the current time and an empty
listing you have to explain to yourself.

Ambiguous numeric dates are refused on purpose: `01/15/2026` and `15.01.2026`
tell nobody but their writer whether `01/02` is January 2nd. Write the date
out and there is nothing left to guess at.

A day named without a time of day runs midnight to midnight, so the same date
on both sides is that whole day:

```sh
histdb --since 2026-01-15 --until 2026-01-15   # everything that day
histdb --since yesterday                       # since yesterday midnight
histdb -F --since "2 weeks ago"                # what you have run lately
```

Naming a day means the whole day. Nothing quietly fills the hour in from the
current clock and drops the morning of the day you asked for.

## Output

The listing is `fc -li`: id, time, command, no header. A star on the id means
the command came from another shell session, the way zsh marks them under
`SHARE_HISTORY`. On a terminal the id, and the `ret` column when asked for,
are green when the command succeeded and red when it failed. `NO_COLOR` turns
that off.

```
10023  2026-08-02 17:36  histdb
10025* 2026-08-02 17:36  setopt share_history
```

`--columns` picks the fields and their order: `id`, `time`, `dur`, `ret`,
`cwd`, `session`, `shell`, `host`, `user`, `tty`, `cmd`, or with `-F`, `runs`,
`last`, `cmd`. The last four come from the session the command ran in.

```sh
histdb --columns cmd            # bare command lines, for feeding other tools
histdb --columns id,time,cwd,cmd
histdb -F --columns runs,cmd
```

A table that fills the default row limit writes a notice to stderr naming `-n`,
so pipes reading stdout do not get an extra row. Passing `-n` makes the limit
explicit and quiet.

`--jsonl` writes one JSON object per line, every column and every match unless
`--columns` or `-n` narrows it:

```sh
histdb --jsonl | jq -r 'select(.ret != 0) | .cmd'
histdb --jsonl -n 100 | jq -r 'select(.host == "box") | .cmd'
```

```json
{
    "id": 1,
    "time": "2026-08-03T07:31:20-05:00",
    "dur": 0.5,
    "ret": 0,
    "cwd": "/tmp",
    "session": "1000.4242.1",
    "shell": "zsh",
    "host": "box",
    "user": "someone",
    "tty": "ttys009",
    "cmd": "git status"
}
```

Times are RFC3339, a command that has not finished yet has `null` for `dur`
and `ret`, and `cwd` is the full path rather than the `~` the table shows. A
command containing a newline stays one line and one record, which is what the
plain listing cannot promise.

The table stops at 20 rows and JSON does not, on the grounds that a reader
wants a page and a program wants the answer. `-n 0` means every match in
either.

## Importing an existing history file

```sh
histdb import zsh $HISTFILE
histdb import bash ~/.bash_history
histdb import --format plain zsh ~/.zsh_history
```

Importing the same file twice adds nothing the second time, so it is safe to
re-run as the file grows. A file is one session, keyed by its path.

A shell writes times into its history file only when asked to, under
`EXTENDED_HISTORY` in zsh or `HISTTIMEFORMAT` in bash, and `--format` says
which kind of file it is: `extended`, `plain`, or `auto` to tell from the first
line.

A history file times commands to the second and records no exit status, so an
imported row has no `ret` and no `dur`, and two commands from the same second
are stored a millisecond apart to keep them both. A `plain` file has no times
at all: its commands are laid out one second apart ending at the file's
modification time, and re-importing counts what is already stored rather than
matching on time, which assumes the file only ever grows. A bash file also has
no elapsed time to import, since bash never writes one.

## Matching

`--like` takes a SQL LIKE pattern, so `%` matches any run of characters and `_`
matches one:

| pattern             | matches                             |
| ------------------- | ----------------------------------- |
| `--like 'git%'`     | commands starting with `git`        |
| `--like '%git%'`    | commands containing `git` anywhere  |
| `--like 'git _ush'` | `git push`, `git rush`              |
| `--like '%50\%%'`   | commands containing a literal `50%` |

The pattern is bound as a parameter, never spliced into the query, so SQL inside
it is only ever text to match against. A backslash escapes a wildcard when you
want it literal.

Note a leading `%` cannot use the index on `cmd`, so anchored patterns are
faster on a large history.

### zsh-autosuggestions

Suggest the command you run most often that starts with what you have typed,
preferring this directory and falling back to anywhere:

```zsh
_zsh_autosuggest_strategy_histdb() {
  typeset -g suggestion
  # What was typed is literal text, so escape the LIKE wildcards in it before
  # anchoring the pattern.
  local q=${1//\\/\\\\}
  q=${q//\%/\\%}
  q=${q//_/\\_}
  suggestion=$(histdb -F --prefer-here --columns cmd -n 1 --like "$q%")
}
ZSH_AUTOSUGGEST_STRATEGY=(histdb)
```

Use `-d` in place of `--prefer-here` to suggest only from this directory, with
no fallback.

## What gets recorded

If you ran it, it is recorded. A shell's history options decide what goes in
that shell's own history file, and histdb leaves those alone: they still apply
to the file, and they are not a reason to drop a row. Duplicates are a matter
for searching and purging, not for recording, so `--no-dups` collapses them at
search time and the rows stay.

The one exception is a leading space. `HIST_IGNORE_SPACE` in zsh and
`HISTCONTROL=ignorespace` in bash are how you say do not record this at all,
and histdb honors that.

## Zsh options

`histdb init zsh` installs the hooks and a small `histdb` function that reads
`$options` on every call, so a `setopt` mid-session takes effect immediately.

| option               | effect                                                                      |
| -------------------- | --------------------------------------------------------------------------- |
| `HIST_IGNORE_SPACE`  | a command starting with a space is not recorded                             |
| `HIST_REDUCE_BLANKS` | runs of whitespace are squeezed before recording                            |
| `SHARE_HISTORY`      | when off, searches are limited to the current session, unless you pass `-S` |
| `HIST_FIND_NO_DUPS`  | searches show each command once, its newest run                             |

Deliberately ignored, and why:

| option                                                    | why                                                                                                                                     |
| --------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `HIST_IGNORE_DUPS`, `HIST_IGNORE_ALL_DUPS`, `HIST_SAVE_NO_DUPS` | all drop duplicates; histdb keeps every run, and `--no-dups` or `HIST_FIND_NO_DUPS` collapses them at search time                  |
| `HIST_NO_FUNCTIONS`, `HIST_NO_STORE`                      | both keep a command out of zsh's history file, which they still do; enabling histdb is itself the choice to keep it in SQLite           |
| `INC_APPEND_HISTORY`, `INC_APPEND_HISTORY_TIME`           | histdb always writes at both ends of a command, so nothing is lost if the shell dies                                                    |
| `HIST_ALLOW_CLOBBER`                                      | rewrites `>` as `>\|` when storing; histdb records the line as typed                                                                    |
| `HIST_EXPIRE_DUPS_FIRST`, `SAVEHIST`                      | trimming policy, and histdb never trims                                                                                                 |
| `EXTENDED_HISTORY`                                        | histdb always stores start time and duration                                                                                            |
| `APPEND_HISTORY`, `HIST_SAVE_BY_COPY`, `HIST_FCNTL_LOCK`  | history-file mechanics, replaced by SQLite                                                                                              |
| `BANG_HIST`, `HIST_VERIFY`, `HIST_BEEP`, `HIST_LEX_WORDS` | line editor behavior, not storage                                                                                                       |

## Bash options

Bash has no `preexec`, so `histdb init bash` installs a `DEBUG` trap and two
`PROMPT_COMMAND` entries: one that finishes the command that just ran, and one
that runs last so nothing the prompt itself does is mistaken for a typed line.
Where [bash-preexec] is already loaded, histdb hooks onto it instead and leaves
the trap where it is.

Bash 5.0 or newer is required. Older bash has no sub-second clock and no
`history -d -1`, both of which the hooks are built on. macOS still ships bash
3.2 as `/bin/bash`, so install a current one with `brew install bash`.

The command recorded is the line bash stored, not `$BASH_COMMAND`, which is one
simple command and would report `true | false` as `true`. The history list is
the only place the whole typed line exists, so anything that stops bash storing
a line also hides it from histdb. To record every command anyway, histdb takes
those settings over: it clears them, lets every line reach the list, and puts
them back afterwards with `history -d`. Your history file ends up filtered the
way you asked, and the SQLite rows are complete.

| setting                    | history file                              | histdb   |
| -------------------------- | ----------------------------------------- | -------- |
| `HISTCONTROL=ignorespace`  | leading-space command left out            | not recorded |
| `HISTCONTROL=ignoredups`   | repeat of the previous command left out   | recorded |
| `HISTCONTROL=erasedups`    | repeat of the previous command left out   | recorded |
| `HISTCONTROL=ignoreboth`   | both of the ignore rules above            | leading space not recorded |
| `HISTIGNORE`               | matching command left out, `&` included   | recorded |

They are read again at every prompt, so one set after histdb loaded works like
one set before it.

`erasedups` is served as `ignoredups`: erasing an older copy means finding it,
and that is a scan of the whole list on every prompt. Turning history off
outright, with `set +o history` or `HISTSIZE=0`, leaves no list to read at all,
so histdb falls back to `$BASH_COMMAND` and a pipeline is recorded as its first
command.

bash-preexec strips `ignorespace` out of `HISTCONTROL` when it loads, so under
it a command typed with a leading space is recorded like any other. That is its
choice, not one histdb can undo without taking the setting away from you twice.

A `DEBUG` trap already installed belongs to something else, so histdb leaves it
alone, says so on stderr, and does not hook. Load histdb before whatever set
the trap, or load bash-preexec so the two can share it.

## Recording

`histdb record` is what the shell hooks call, not something to run by hand. A
command is one row: inserted before it runs, so anything that outlives the
shell is already on disk, then updated afterward with its exit status and end
time. The session key and start time identify the row in both writes.

Those writes are separate processes, and for a command that returns instantly
the second one often reaches the database first, so the update inserts when it
finds no row yet and the late insert then fills in what it knows.

A command still running is hidden from the shell that launched it, since it
would otherwise be the newest row in its own output. Another shell's in-flight
command shows with `-` for duration and status.

Each command carries a `meta` column, a JSON object for anything histdb does
not model. Query it with `json_extract(meta, '$.key')`.

## Development

```sh
just            # list recipes
just check      # fmt, vet, test
just run init zsh
```

## License

[MIT](LICENSE)
