package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/mattmc3/getopt"

	"github.com/mattmc3/histdb/internal/history"
)

func runSearch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	// getopt prints its own errors and usage, so silence it and own both here.
	fs := getopt.NewFlagSet("histdb", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	showHelp := fs.Define("help", false, "print this help")
	showVersion := fs.Define("version", false, "print version")
	here := fs.Define("here", false, "only commands run in this directory")
	repo := fs.Define("repo", false, "only commands run anywhere in this repository")
	unique := fs.Define("no-dups", false, "only the newest run of each command")
	failed := fs.Define("fail", false, "only commands that failed")
	succeeded := fs.Define("success", false, "only commands that succeeded")
	thisSession := fs.Define("session", false, "only commands from this shell session")
	like := fs.Define("like", "", "SQL LIKE pattern, wildcards as written")
	head := fs.Define("head", false, "oldest matches instead of newest")
	limit := fs.Define("limit", history.DefaultLimit, "rows to show")
	byFrequency := fs.Define("sort-by-frequency", false, "most run commands first")
	preferHere := fs.Define("prefer-here", false, "rank this directory's commands first")
	plain := fs.Define("plain", false, "print command lines only")
	fs.Aliases(
		"h", "help",
		"v", "version",
		"d", "here",
		"r", "repo",
		"f", "fail",
		"s", "success",
		"S", "session",
		"H", "head",
		"n", "limit",
		"F", "sort-by-frequency",
	)

	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, usage)
		return err
	}

	switch {
	case *showHelp:
		fmt.Fprint(stdout, usage)
		return nil
	case *showVersion:
		fmt.Fprintf(stdout, "histdb %s\n", version)
		return nil
	case *failed && *succeeded:
		return errors.New("--fail and --success are mutually exclusive")
	case *head && *byFrequency:
		return errors.New("--head and --sort-by-frequency are mutually exclusive")
	case *preferHere && !*byFrequency:
		return errors.New("--prefer-here needs --sort-by-frequency")
	case len(fs.Args()) > 0:
		// Matching is always an explicit pattern, so a bare word is a mistake
		// rather than a substring search.
		return fmt.Errorf("unexpected argument %q, match with --like '%%%s%%'",
			fs.Args()[0], fs.Args()[0])
	}

	filter := history.Filter{
		Like:              *like,
		Oldest:            *head,
		Limit:             *limit,
		Unique:            *unique,
		CurrentSessionKey: os.Getenv("HISTDB_SESSION"),
	}
	switch {
	case *failed:
		filter.Status = history.Failed
	case *succeeded:
		filter.Status = history.Succeeded
	}
	if *here || *repo || *preferHere {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		if *preferHere {
			filter.PreferCwd = wd
		}
		if *here || *repo {
			filter.Cwd = wd
			// Outside a checkout there is no wider scope, so --repo is --here.
			if *repo {
				filter.VCSRoot = vcsRootOf(wd)
			}
		}
	}
	if *thisSession {
		filter.SessionKey = os.Getenv("HISTDB_SESSION")
		if filter.SessionKey == "" {
			return errors.New("--session: HISTDB_SESSION is not set")
		}
	}

	path := dbPath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no database at %s", path)
	}
	store, err := history.Open(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()

	// Ranking by frequency collapses runs into one row per command, so there is
	// no single time, directory or exit status left to show.
	if *byFrequency {
		frequent, err := store.Entries().MostFrequent(ctx, filter)
		if err != nil {
			return err
		}
		return renderFrequent(stdout, frequent, *plain)
	}

	entries, err := store.Entries().Search(ctx, filter)
	if err != nil {
		return err
	}
	return renderEntries(stdout, entries, *plain)
}

func renderEntries(w io.Writer, entries []history.Entry, plain bool) error {
	if plain {
		for _, e := range entries {
			fmt.Fprintln(w, e.Cmd)
		}
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "time\tdur\tret\tcwd\tcmd")
	for _, e := range entries {
		dur, ret := "-", "-"
		if e.Finished() {
			dur = fmt.Sprintf("%.2f", e.Duration().Seconds())
			ret = fmt.Sprint(e.Ret)
		}
		// Stored UTC, shown in the shell's own timezone.
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			e.StartAt.Local().Format("2006-01-02 15:04:05"),
			dur, ret, shortenHome(e.Cwd), e.Cmd)
	}
	return tw.Flush()
}

func renderFrequent(w io.Writer, frequent []history.Frequent, plain bool) error {
	if plain {
		for _, c := range frequent {
			fmt.Fprintln(w, c.Cmd)
		}
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "runs\tlast\tcmd")
	for _, c := range frequent {
		fmt.Fprintf(tw, "%d\t%s\t%s\n",
			c.Count, c.LastAt.Local().Format("2006-01-02 15:04:05"), c.Cmd)
	}
	return tw.Flush()
}

func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(path, home) {
		return path
	}
	return "~" + strings.TrimPrefix(path, home)
}
