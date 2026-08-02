package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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
	columns := fs.Define("columns", "", "comma separated columns to print")
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

	// Ranking by frequency collapses runs into one row per command, so it has
	// columns of its own: there is no single time or exit status left to show.
	if *byFrequency {
		cols, err := freqColumns(cmp.Or(*columns, defaultFreqColumns))
		if err != nil {
			return err
		}
		frequent, err := store.Entries().MostFrequent(ctx, filter)
		if err != nil {
			return err
		}
		return renderFrequent(stdout, frequent, cols)
	}

	cols, err := entryColumns(cmp.Or(*columns, defaultColumns))
	if err != nil {
		return err
	}
	entries, err := store.Entries().Search(ctx, filter)
	if err != nil {
		return err
	}
	return renderEntries(stdout, entries, cols, os.Getenv("HISTDB_SESSION"), useColor(stdout))
}

// The listing is `fc -li`: no header, one row per command, columns padded to
// the widest cell in the set. Times are stored UTC and shown local.
const (
	defaultColumns     = "id,time,cmd"
	defaultFreqColumns = "runs,last,cmd"
	timeFormat         = "2006-01-02 15:04"
)

// A column of the listing. right-aligns the numeric ones, the way fc lines up
// its ids.
type column[T any] struct {
	name  string
	right bool
	cell  func(T) string
}

var entryColumnSet = []column[history.Entry]{
	{"id", true, func(e history.Entry) string { return strconv.FormatInt(e.ID, 10) }},
	{"time", false, func(e history.Entry) string { return e.StartAt.Local().Format(timeFormat) }},
	{"dur", true, func(e history.Entry) string {
		if !e.Finished() {
			return "-"
		}
		return fmt.Sprintf("%.2f", e.Duration().Seconds())
	}},
	{"ret", true, func(e history.Entry) string {
		if !e.Finished() {
			return "-"
		}
		return strconv.Itoa(e.Ret)
	}},
	{"cwd", false, func(e history.Entry) string { return shortenHome(e.Cwd) }},
	{"session", false, func(e history.Entry) string { return e.Session.Key }},
	{"cmd", false, func(e history.Entry) string { return e.Cmd }},
}

var freqColumnSet = []column[history.Frequent]{
	{"runs", true, func(f history.Frequent) string { return strconv.Itoa(f.Count) }},
	{"last", false, func(f history.Frequent) string { return f.LastAt.Local().Format(timeFormat) }},
	{"cmd", false, func(f history.Frequent) string { return f.Cmd }},
}

func entryColumns(spec string) ([]column[history.Entry], error) {
	return pickColumns(spec, entryColumnSet)
}

func freqColumns(spec string) ([]column[history.Frequent], error) {
	return pickColumns(spec, freqColumnSet)
}

func pickColumns[T any](spec string, set []column[T]) ([]column[T], error) {
	var picked []column[T]
	for _, name := range strings.Split(spec, ",") {
		name = strings.TrimSpace(name)
		i := slices.IndexFunc(set, func(c column[T]) bool { return c.name == name })
		if i < 0 {
			names := make([]string, len(set))
			for j, c := range set {
				names[j] = c.name
			}
			return nil, fmt.Errorf("unknown column %q, want one of: %s",
				name, strings.Join(names, ", "))
		}
		picked = append(picked, set[i])
	}
	return picked, nil
}

func renderEntries(w io.Writer, entries []history.Entry, cols []column[history.Entry], session string, color bool) error {
	// Exit status shows as a color on the two columns that stand for it, and
	// the id also carries zsh's star for a command from another session.
	decorate := func(name, cell string, e history.Entry) string {
		if color && e.Finished() && (name == "id" || name == "ret") {
			code := "31"
			if e.Ret == 0 {
				code = "32"
			}
			cell = "\x1b[" + code + "m" + cell + "\x1b[0m"
		}
		if name != "id" {
			return cell
		}
		if session != "" && e.Session.Key != session {
			return cell + "*"
		}
		return cell + " "
	}
	return render(w, entries, cols, decorate)
}

func renderFrequent(w io.Writer, frequent []history.Frequent, cols []column[history.Frequent]) error {
	return render(w, frequent, cols, nil)
}

// render lays the rows out in fixed-width columns. decorate, when given, marks
// cells up after they are padded, so the marks cannot skew widths.
func render[T any](w io.Writer, rows []T, cols []column[T], decorate func(string, string, T) string) error {
	cells := make([][]string, len(rows))
	widths := make([]int, len(cols))
	for i, row := range rows {
		cells[i] = make([]string, len(cols))
		for j, c := range cols {
			cells[i][j] = c.cell(row)
			widths[j] = max(widths[j], utf8.RuneCountInString(cells[i][j]))
		}
	}

	var line strings.Builder
	for i, row := range rows {
		line.Reset()
		for j, c := range cols {
			cell := pad(cells[i][j], widths[j], c.right)
			sep := "  "
			if decorate != nil {
				cell = decorate(c.name, cell, row)
				// The star takes one of the two spaces after the id.
				if c.name == "id" {
					sep = " "
				}
			}
			line.WriteString(cell)
			if j < len(cols)-1 {
				line.WriteString(sep)
			}
		}
		if _, err := fmt.Fprintln(w, strings.TrimRight(line.String(), " ")); err != nil {
			return err
		}
	}
	return nil
}

func pad(s string, width int, right bool) string {
	fill := strings.Repeat(" ", max(0, width-utf8.RuneCountInString(s)))
	if right {
		return fill + s
	}
	return s + fill
}

// Color is for a terminal to read, not for whatever a pipe leads to.
func useColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(path, home) {
		return path
	}
	return "~" + strings.TrimPrefix(path, home)
}
