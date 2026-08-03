package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
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
	thisSession := fs.Define("session", false, "only commands from this shell session")
	allSessions := fs.Define("all-sessions", false, "commands from every shell session")
	like := fs.Define("like", "", "SQL LIKE pattern, wildcards as written")
	head := fs.Define("head", false, "oldest matches instead of newest")
	limit := fs.Define("limit", history.DefaultLimit, "rows to show, 0 for every match")
	byFrequency := fs.Define("sort-by-frequency", false, "most run commands first")
	preferHere := fs.Define("prefer-here", false, "rank this directory's commands first")
	columns := fs.Define("columns", "", "comma separated columns to print")
	jsonl := fs.Define("jsonl", false, "one JSON object per line")
	fs.Aliases(
		"h", "help",
		"v", "version",
		"d", "here",
		"r", "repo",
		"s", "session",
		"S", "all-sessions",
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
		Limit:             rowLimit(fs, *limit, *jsonl),
		Unique:            *unique,
		CurrentSessionKey: os.Getenv("HISTDB_SESSION"),
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
	// The wrapper passes --session for you when SHARE_HISTORY is off, so an
	// --all-sessions on the command line is the caller overriding it.
	if *thisSession && !*allSessions {
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
		// JSON is read by a program, so it defaults to every column rather
		// than the three a person wants to look at.
		spec := cmp.Or(*columns, defaultFreqColumns)
		if *jsonl && *columns == "" {
			spec = allColumns(freqColumnSet)
		}
		cols, err := freqColumns(spec)
		if err != nil {
			return err
		}
		frequent, err := store.Entries().MostFrequent(ctx, filter)
		if err != nil {
			return err
		}
		if *jsonl {
			return renderJSONL(stdout, frequent, cols)
		}
		return renderFrequent(stdout, frequent, cols)
	}

	spec := cmp.Or(*columns, defaultColumns)
	if *jsonl && *columns == "" {
		spec = allColumns(entryColumnSet)
	}
	cols, err := entryColumns(spec)
	if err != nil {
		return err
	}
	entries, err := store.Entries().Search(ctx, filter)
	if err != nil {
		return err
	}
	if *jsonl {
		return renderJSONL(stdout, entries, cols)
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
// its ids. cell is what a reader sees, value what a program gets: a dash is a
// missing number to one and null to the other.
type column[T any] struct {
	name  string
	right bool
	cell  func(T) string
	value func(T) any
}

var entryColumnSet = []column[history.Entry]{
	{"id", true,
		func(e history.Entry) string { return strconv.FormatInt(e.ID, 10) },
		func(e history.Entry) any { return e.ID }},
	{"time", false,
		func(e history.Entry) string { return e.StartAt.Local().Format(timeFormat) },
		func(e history.Entry) any { return e.StartAt.Local().Format(time.RFC3339) }},
	{"dur", true,
		func(e history.Entry) string {
			if !e.Finished() {
				return "-"
			}
			return fmt.Sprintf("%.2f", e.Duration().Seconds())
		},
		func(e history.Entry) any {
			if !e.Finished() {
				return nil
			}
			return e.Duration().Seconds()
		}},
	{"ret", true,
		func(e history.Entry) string {
			if !e.Finished() {
				return "-"
			}
			return strconv.Itoa(e.Ret)
		},
		func(e history.Entry) any {
			if !e.Finished() {
				return nil
			}
			return e.Ret
		}},
	{"cwd", false,
		func(e history.Entry) string { return shortenHome(e.Cwd) },
		func(e history.Entry) any { return e.Cwd }},
	{"session", false,
		func(e history.Entry) string { return e.Session.Key },
		func(e history.Entry) any { return e.Session.Key }},
	{"shell", false,
		func(e history.Entry) string { return e.Session.Shell },
		func(e history.Entry) any { return e.Session.Shell }},
	{"host", false,
		func(e history.Entry) string { return e.Session.Host },
		func(e history.Entry) any { return e.Session.Host }},
	{"user", false,
		func(e history.Entry) string { return e.Session.User },
		func(e history.Entry) any { return e.Session.User }},
	{"tty", false,
		func(e history.Entry) string { return e.Session.TTY },
		func(e history.Entry) any { return e.Session.TTY }},
	{"cmd", false,
		func(e history.Entry) string { return e.Cmd },
		func(e history.Entry) any { return e.Cmd }},
}

var freqColumnSet = []column[history.Frequent]{
	{"runs", true,
		func(f history.Frequent) string { return strconv.Itoa(f.Count) },
		func(f history.Frequent) any { return f.Count }},
	{"last", false,
		func(f history.Frequent) string { return f.LastAt.Local().Format(timeFormat) },
		func(f history.Frequent) any { return f.LastAt.Local().Format(time.RFC3339) }},
	{"cmd", false,
		func(f history.Frequent) string { return f.Cmd },
		func(f history.Frequent) any { return f.Cmd }},
}

// renderJSONL writes one object per row, keys in the order the columns were
// asked for.
func renderJSONL[T any](w io.Writer, rows []T, cols []column[T]) error {
	var line strings.Builder
	for _, row := range rows {
		line.Reset()
		line.WriteByte('{')
		for i, c := range cols {
			if i > 0 {
				line.WriteByte(',')
			}
			key, err := jsonValue(c.name)
			if err != nil {
				return err
			}
			value, err := jsonValue(c.value(row))
			if err != nil {
				return err
			}
			line.WriteString(key)
			line.WriteByte(':')
			line.WriteString(value)
		}
		line.WriteByte('}')
		if _, err := fmt.Fprintln(w, line.String()); err != nil {
			return err
		}
	}
	return nil
}

// jsonValue encodes one value, leaving &, < and > as themselves: this is a
// pipe, not a web page.
func jsonValue(v any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// rowLimit reads -n. Zero asks for every match, and JSON with no -n at all is
// taken the same way: a program wants the whole answer, a reader wants a page
// of it.
func rowLimit(fs *getopt.FlagSet, limit int, jsonl bool) int {
	if limit == 0 {
		return history.NoLimit
	}
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "limit" {
			given = true
		}
	})
	if jsonl && !given {
		return history.NoLimit
	}
	return limit
}

func allColumns[T any](set []column[T]) string {
	names := make([]string, len(set))
	for i, c := range set {
		names[i] = c.name
	}
	return strings.Join(names, ",")
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
