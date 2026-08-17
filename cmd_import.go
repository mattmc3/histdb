package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mattmc3/getopt"

	"github.com/mattmc3/histdb/internal/history"
)

// importer reads one shell's history file. timeOption names the setting that
// makes that shell write timestamps, for the note an untimed import prints.
type importer struct {
	parse      func(io.Reader, string) ([]importedEntry, error)
	timeOption string
}

// importers maps a shell to its history file parser. Adding a shell means a
// parser and an entry here.
var importers = map[string]importer{
	"bash": {parseBashHistory, "HISTTIMEFORMAT"},
	"zsh":  {parseZshHistory, "EXTENDED_HISTORY"},
}

// Formats a history file comes in: timestamped, bare command lines, or work
// it out from the file.
const (
	formatAuto     = "auto"
	formatExtended = "extended"
	formatPlain    = "plain"
)

// importedEntry is what a history file says about a command. start is zero in
// a plain file, which records no times at all.
type importedEntry struct {
	start   time.Time
	elapsed int
	cmd     string
}

func runImport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := getopt.NewFlagSet("histdb import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	format := fs.Define("format", formatAuto, "history file format")
	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, usage)
		return err
	}
	switch *format {
	case formatAuto, formatExtended, formatPlain:
	default:
		return fmt.Errorf("unknown format %q, want one of: %s, %s, %s",
			*format, formatAuto, formatExtended, formatPlain)
	}
	if len(fs.Args()) != 2 {
		return fmt.Errorf("usage: histdb import [--format %s|%s] <%s> <file>",
			formatExtended, formatPlain, strings.Join(importShells(), "|"))
	}
	shell, path := fs.Args()[0], fs.Args()[1]

	shellImporter, ok := importers[shell]
	if !ok {
		return fmt.Errorf("unsupported shell %q, want one of: %s",
			shell, strings.Join(importShells(), ", "))
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	entries, err := shellImporter.parse(file, *format)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "no commands in %s\n", path)
		return nil
	}

	store, err := history.Open(ctx, dbPath())
	if err != nil {
		return err
	}
	defer store.Close()

	// One session per file, so importing it again lands on the same rows
	// rather than a second copy of them.
	session := history.Session{
		Key:   "import:" + shell + ":" + path,
		Shell: shell,
		Host:  shortHostname(),
	}
	if who, err := user.Current(); err == nil {
		session.User = who.Username
	}
	session.StartAt = entries[0].start
	if err := store.Sessions().Ensure(ctx, &session); err != nil {
		return err
	}

	untimed := entries[0].start.IsZero()
	if untimed {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		// Nothing in the file says when anything ran, so the entries are laid
		// out a second apart, ending when the file was last written.
		already, err := store.Entries().CountForSession(ctx, session.ID)
		if err != nil {
			return err
		}
		entries = datedFromMtime(entries, info.ModTime(), already)
	}

	rows := make([]history.Entry, len(entries))
	for i, e := range entries {
		rows[i] = history.Entry{
			Session: session,
			Cmd:     e.cmd,
			StartAt: e.start,
			Meta:    importMeta(e.elapsed, untimed),
		}
	}
	written, err := store.Entries().Import(ctx, rows)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "imported %s, skipped %d already stored\n",
		plural(written, "command"), len(rows)-written)
	if untimed {
		fmt.Fprintf(stdout, "%s has no timestamps, so times are approximate: "+
			"set %s in %s to record them\n", path, shellImporter.timeOption, shell)
	}
	return nil
}

// datedFromMtime drops the entries already imported and dates the rest, one
// second apart, ending at mtime. A file with no times has only its order.
func datedFromMtime(entries []importedEntry, mtime time.Time, already int) []importedEntry {
	if already >= len(entries) {
		return nil
	}
	entries = entries[already:]
	for i := range entries {
		entries[i].start = mtime.Add(-time.Duration(len(entries)-1-i) * time.Second)
	}
	return entries
}

func importShells() []string {
	return slices.Sorted(maps.Keys(importers))
}

func importMeta(elapsed int, approximate bool) string {
	meta := map[string]any{}
	if elapsed > 0 {
		meta["elapsed"] = elapsed
	}
	if approximate {
		meta["approximate_time"] = true
	}
	if len(meta) == 0 {
		return ""
	}
	text, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(text)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// parseZshHistory reads a zsh history file. EXTENDED_HISTORY writes each
// command as `: <start>:<elapsed>;<command>`, and without it the line is the
// command alone. Either way a newline inside a command is held over with a
// backslash.
func parseZshHistory(r io.Reader, format string) ([]importedEntry, error) {
	var entries []importedEntry
	var record strings.Builder

	scanner := bufio.NewScanner(r)
	// A command can be far longer than a line of source.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasSuffix(line, `\`) {
			record.WriteString(strings.TrimSuffix(line, `\`))
			record.WriteString("\n")
			continue
		}
		record.WriteString(line)
		text := record.String()
		record.Reset()

		if strings.TrimSpace(text) == "" {
			continue
		}
		e, timed := parseZshRecord(text)
		if format == formatAuto && len(entries) == 0 {
			format = formatExtended
			if !timed {
				format = formatPlain
			}
		}
		if format == formatPlain {
			entries = append(entries, importedEntry{cmd: text})
			continue
		}
		if !timed {
			return nil, fmt.Errorf("no timestamp on %q, try --format %s",
				text, formatPlain)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	if format == formatExtended && len(entries) == 0 {
		return nil, errors.New("no timestamps found, try --format " + formatPlain)
	}
	return entries, nil
}

// parseBashHistory reads a bash history file. HISTTIMEFORMAT makes bash write
// `#<start>` on its own line ahead of each command, and without it the line is
// the command alone. A command can span lines, so everything up to the next
// timestamp belongs to the one before it. Bash records no elapsed time.
func parseBashHistory(r io.Reader, format string) ([]importedEntry, error) {
	var entries []importedEntry
	var lines []string
	var start time.Time

	// A timestamped command is only complete once the next timestamp shows up,
	// or the file ends.
	flush := func() {
		if len(lines) == 0 {
			return
		}
		cmd := strings.Join(lines, "\n")
		lines = nil
		if strings.TrimSpace(cmd) != "" {
			entries = append(entries, importedEntry{start: start, cmd: cmd})
		}
	}

	scanner := bufio.NewScanner(r)
	// A command can be far longer than a line of source.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		at, timed := parseBashTimestamp(line)
		if format == formatAuto && strings.TrimSpace(line) != "" {
			format = formatPlain
			if timed {
				format = formatExtended
			}
		}
		if format == formatPlain {
			if strings.TrimSpace(line) != "" {
				entries = append(entries, importedEntry{cmd: line})
			}
			continue
		}
		if timed {
			flush()
			start = at
			continue
		}
		if start.IsZero() {
			if strings.TrimSpace(line) == "" {
				continue
			}
			return nil, fmt.Errorf("no timestamp on %q, try --format %s",
				line, formatPlain)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	flush()

	if format == formatExtended && len(entries) == 0 {
		return nil, errors.New("no timestamps found, try --format " + formatPlain)
	}
	return entries, nil
}

// parseBashTimestamp reads bash's `#<epoch>` header. A command that starts with
// a `#` and is not all digits is a comment, not a header.
func parseBashTimestamp(line string) (time.Time, bool) {
	digits, ok := strings.CutPrefix(line, "#")
	if !ok {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// parseZshRecord pulls apart `: <start>:<elapsed>;<command>`. Anything else,
// a command that merely looks like a header included, is not one.
func parseZshRecord(record string) (importedEntry, bool) {
	rest, ok := strings.CutPrefix(record, ": ")
	if !ok {
		return importedEntry{}, false
	}
	startText, rest, ok := strings.Cut(rest, ":")
	if !ok {
		return importedEntry{}, false
	}
	elapsedText, cmd, ok := strings.Cut(rest, ";")
	if !ok {
		return importedEntry{}, false
	}

	start, err := strconv.ParseInt(strings.TrimSpace(startText), 10, 64)
	if err != nil {
		return importedEntry{}, false
	}
	elapsed, err := strconv.Atoi(strings.TrimSpace(elapsedText))
	if err != nil {
		return importedEntry{}, false
	}
	return importedEntry{start: time.Unix(start, 0), elapsed: elapsed, cmd: cmd}, true
}
