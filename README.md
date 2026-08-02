# histdb

Shell history in SQLite.

TODO: Placeholder

## Install

```sh
go install github.com/mattmc3/histdb@latest
```

Or build from a clone:

```sh
just build      # -> bin/histdb
just install    # -> $GOBIN
```

Enable in Zsh by adding this to `.zshrc`:

```zsh
source <(histdb init zsh)
```

## Usage

Bare `histdb` lists the last 20 commands, oldest first. Matching is always an
explicit `--like` pattern.

```sh
histdb                # recent commands
histdb --like '%git%' # commands containing "git"
histdb --like 'git%'  # commands starting with "git"
histdb -d             # only this directory
histdb -r             # this directory and the rest of its repository
histdb -f             # only commands that failed
histdb -s             # only commands that succeeded
histdb -S             # only this shell session
histdb -H             # oldest matches instead of newest
histdb -n 100         # more rows
histdb --no-dups      # only the newest run of each command
```

Long forms: `--here`, `--repo`, `--fail`, `--success`, `--session`, `--head`,
`--limit`. Outside a checkout, `-r` behaves like `-d`.

| variable         | meaning                                                  |
| ---------------- | -------------------------------------------------------- |
| `HISTDB_FILE`    | database path, default `$XDG_DATA_HOME/histdb/histdb.db` |
| `HISTDB_SESSION` | session key, set by the shell integration                |
| `HISTDB_BIN`     | binary the hooks call, pinned by `histdb init`           |

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
`cwd`, `session`, `cmd`, or with `-F`, `runs`, `last`, `cmd`.

```sh
histdb --columns cmd            # bare command lines, for feeding other tools
histdb --columns id,time,cwd,cmd
histdb -F --columns runs,cmd
```

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

## Zsh options

`histdb init zsh` installs the hooks and a small `histdb` function that reads
`$options` on every call, so a `setopt` mid-session takes effect immediately.

| option               | effect                                                  |
| -------------------- | ------------------------------------------------------- |
| `HIST_IGNORE_SPACE`  | a command starting with a space is not recorded         |
| `HIST_REDUCE_BLANKS` | runs of whitespace are squeezed before recording        |
| `HIST_IGNORE_DUPS`   | a command identical to the previous one is not recorded |
| `HIST_NO_FUNCTIONS`  | function definitions are not recorded                   |
| `HIST_NO_STORE`      | `history` and `fc` are not recorded                     |
| `SHARE_HISTORY`      | when off, searches are limited to the current session   |
| `HIST_FIND_NO_DUPS`  | searches show each command once, its newest run         |

Deliberately ignored, and why:

| option                                                    | why                                                                                                                                     |
| --------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `HIST_IGNORE_ALL_DUPS`, `HIST_SAVE_NO_DUPS`               | both drop older duplicates; histdb keeps everything, but you can use `--no-dups` or `HIST_FIND_NO_DUPS` to collapse them at search time |
| `INC_APPEND_HISTORY`, `INC_APPEND_HISTORY_TIME`           | histdb always writes at both ends of a command, so nothing is lost if the shell dies                                                    |
| `HIST_ALLOW_CLOBBER`                                      | rewrites `>` as `>\|` when storing; histdb records the line as typed                                                                    |
| `HIST_EXPIRE_DUPS_FIRST`, `SAVEHIST`                      | trimming policy, and histdb never trims                                                                                                 |
| `EXTENDED_HISTORY`                                        | histdb always stores start time and duration                                                                                            |
| `APPEND_HISTORY`, `HIST_SAVE_BY_COPY`, `HIST_FCNTL_LOCK`  | history-file mechanics, replaced by SQLite                                                                                              |
| `BANG_HIST`, `HIST_VERIFY`, `HIST_BEEP`, `HIST_LEX_WORDS` | line editor behavior, not storage                                                                                                       |

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
