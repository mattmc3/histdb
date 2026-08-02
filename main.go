package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var version = "0.0.1"

const usage = `histdb - shell history in SQLite

usage:
  histdb [options]             search history, same as histdb search
  histdb init <shell>          print shell integration to eval
  histdb record [options]      record one command, called by the shell hooks

search options:
  -d, --here       only commands run in this directory
  -r, --repo       only commands run anywhere in this repository
  -f, --fail       only commands that failed
  -s, --success    only commands that succeeded
  -S, --session    only commands from this shell session
      --like P     match commands against a SQL LIKE pattern
  -H, --head       oldest matches instead of newest
  -n, --limit N    rows to show (default 20)
      --no-dups    only the newest run of each command

ranking:
  -F, --sort-by-frequency   most run commands first, one row per command
      --prefer-here         with -F, rank this directory's commands first

output:
      --columns C  comma separated columns, in the order given
                   id, time, dur, ret, cwd, session, cmd (default id,time,cmd)
                   with -F: runs, last, cmd (default runs,last,cmd)

  -h, --help       print this help
  -v, --version    print version

The id is starred when the command came from another shell session. On a
terminal the id and ret are colored by exit status; NO_COLOR turns that off.

environment:
  HISTDB_FILE      database path (default $XDG_DATA_HOME/histdb/histdb.db)
  HISTDB_SESSION   session key, set by the shell integration

supported shells: zsh

enable in zsh:
  source <(histdb init zsh)
`

func main() {
	ctx := context.Background()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "histdb: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "init":
			return runInit(args[1:], stdout)
		case "record":
			return runRecord(ctx, args[1:], stderr)
		case "search":
			return runSearch(ctx, args[1:], stdout, stderr)
		}
	}
	return runSearch(ctx, args, stdout, stderr)
}

func dbPath() string {
	if p := os.Getenv("HISTDB_FILE"); p != "" {
		return p
	}

	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "histdb.db"
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "histdb", "histdb.db")
}
