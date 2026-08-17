package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattmc3/histdb/internal/history"
)

// What zsh writes with EXTENDED_HISTORY: a header of start time and elapsed
// seconds, an embedded newline held over with a backslash, utf-8 as itself,
// and no escaping of a semicolon inside the command.
const extendedHistfile = `: 1700000000:0;echo one
: 1700000005:2;sleep 2
: 1700000010:0;echo 'first\
second'
: 1700000015:0;echo café
: 1700000020:0;echo ': 999:0;not a header'
`

func writeHistfile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "zsh_history")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write histfile: %v", err)
	}
	return path
}

func TestImportZsh(t *testing.T) {
	useTempDB(t)
	path := writeHistfile(t, extendedHistfile)

	stdout, _, err := exec(t, "import", "zsh", path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(stdout, "imported 5 commands") {
		t.Errorf("stdout = %q, want a count of 5", stdout)
	}

	got, _, err := exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, want := range []string{
		"echo one",
		"sleep 2",
		"echo 'first\nsecond'",
		"echo café",
		"echo ': 999:0;not a header'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("search output missing %q:\n%s", want, got)
		}
	}
}

// Importing the same file again must not double up, which is what makes a
// re-import after more history safe.
func TestImportZshSkipsDuplicates(t *testing.T) {
	useTempDB(t)
	path := writeHistfile(t, ": 1700000000:0;echo one\n: 1700000005:0;echo two\n")

	if _, _, err := exec(t, "import", "zsh", path); err != nil {
		t.Fatalf("import: %v", err)
	}
	stdout, _, err := exec(t, "import", "zsh", path)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !strings.Contains(stdout, "imported 0 commands") ||
		!strings.Contains(stdout, "skipped 2") {
		t.Errorf("stdout = %q, want nothing imported and 2 skipped", stdout)
	}

	got, _, err := exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "echo one\necho two\n"; got != want {
		t.Errorf("search output = %q, want %q", got, want)
	}
}

// Different commands in one second are all real history, so all of them are
// kept even though the file times them to the second.
func TestImportZshKeepsEveryCommandInASecond(t *testing.T) {
	useTempDB(t)
	path := writeHistfile(t,
		": 1700000000:0;first\n: 1700000000:0;second\n: 1700000000:0;third\n")

	stdout, _, err := exec(t, "import", "zsh", path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(stdout, "imported 3 commands") {
		t.Errorf("stdout = %q, want 3 imported", stdout)
	}

	got, _, err := exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "first\nsecond\nthird\n"; got != want {
		t.Errorf("search output = %q, want %q", got, want)
	}

	// The same file again is still the same three commands.
	if _, _, err := exec(t, "import", "zsh", path); err != nil {
		t.Fatalf("second import: %v", err)
	}
	got, _, err = exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "first\nsecond\nthird\n"; got != want {
		t.Errorf("search output = %q, want %q", got, want)
	}
}

// Without EXTENDED_HISTORY the file is bare command lines, which have an
// order but no times.
func TestImportZshPlainFormat(t *testing.T) {
	useTempDB(t)
	path := writeHistfile(t, "echo one\necho two\necho three\n")

	stdout, _, err := exec(t, "import", "zsh", path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(stdout, "imported 3 commands") {
		t.Errorf("stdout = %q, want 3 imported", stdout)
	}
	if !strings.Contains(stdout, "times are approximate") {
		t.Errorf("stdout = %q, want the times called out as approximate", stdout)
	}

	got, _, err := exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "echo one\necho two\necho three\n"; got != want {
		t.Errorf("search output = %q, want %q", got, want)
	}
}

// A plain file has no times to recognize, so what has already been imported
// is counted instead.
func TestImportZshPlainSkipsWhatIsStored(t *testing.T) {
	useTempDB(t)
	path := writeHistfile(t, "echo one\necho two\n")

	if _, _, err := exec(t, "import", "zsh", path); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := os.WriteFile(path, []byte("echo one\necho two\necho three\n"), 0o600); err != nil {
		t.Fatalf("append: %v", err)
	}
	stdout, _, err := exec(t, "import", "zsh", path)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !strings.Contains(stdout, "imported 1 command,") {
		t.Errorf("stdout = %q, want only the new command imported", stdout)
	}

	got, _, err := exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "echo one\necho two\necho three\n"; got != want {
		t.Errorf("search output = %q, want %q", got, want)
	}
}

func TestImportFormatFlag(t *testing.T) {
	useTempDB(t)

	t.Run("extended on a plain file is an error", func(t *testing.T) {
		path := writeHistfile(t, "echo one\n")

		_, _, err := exec(t, "import", "--format", "extended", "zsh", path)
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "no timestamp") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("plain on an extended file takes the lines whole", func(t *testing.T) {
		useTempDB(t)
		path := writeHistfile(t, ": 1700000000:0;echo one\n")

		if _, _, err := exec(t, "import", "--format", "plain", "zsh", path); err != nil {
			t.Fatalf("import: %v", err)
		}
		got, _, err := exec(t, "--columns", "cmd")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := ": 1700000000:0;echo one\n"; got != want {
			t.Errorf("search output = %q, want %q", got, want)
		}
	})

	t.Run("unknown format", func(t *testing.T) {
		path := writeHistfile(t, "echo one\n")

		_, _, err := exec(t, "import", "--format", "nope", "zsh", path)
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), `unknown format "nope"`) {
			t.Errorf("err = %v", err)
		}
	})
}

// The session records the host the way the hooks do, short of its domain.
func TestImportRecordsShortHostname(t *testing.T) {
	db := useTempDB(t)
	path := writeHistfile(t, ": 1700000000:0;echo one\n")

	if _, _, err := exec(t, "import", "zsh", path); err != nil {
		t.Fatalf("import: %v", err)
	}

	store, err := history.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	entries, err := store.Entries().Search(context.Background(), history.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored %d entries, want 1", len(entries))
	}
	if got, want := entries[0].Session.Host, shortHostname(); got != want {
		t.Errorf("host = %q, want %q", got, want)
	}
}

func TestImportUnsupportedShell(t *testing.T) {
	useTempDB(t)
	path := writeHistfile(t, extendedHistfile)

	_, _, err := exec(t, "import", "fish", path)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), `unsupported shell "fish"`) {
		t.Errorf("err = %v", err)
	}
}

func TestImportRequiresShellAndFile(t *testing.T) {
	useTempDB(t)

	for _, args := range [][]string{{"import"}, {"import", "zsh"}} {
		if _, _, err := exec(t, args...); err == nil {
			t.Errorf("%v: want error, got nil", args)
		}
	}
}

func TestImportMissingFile(t *testing.T) {
	useTempDB(t)

	_, _, err := exec(t, "import", "zsh", filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

// What bash writes with HISTTIMEFORMAT set: a `#<start>` line ahead of each
// command, a multi-line command laid out as it was typed, no elapsed time
// anywhere, and a comment that is a command like any other.
const bashHistfile = `#1700000000
echo one
#1700000005
echo 'first
second'
#1700000010
echo café
#1700000015
# not a header
`

func TestImportBash(t *testing.T) {
	useTempDB(t)
	path := writeHistfile(t, bashHistfile)

	stdout, _, err := exec(t, "import", "bash", path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(stdout, "imported 4 commands") {
		t.Errorf("stdout = %q, want a count of 4", stdout)
	}

	got, _, err := exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, want := range []string{
		"echo one",
		"echo 'first\nsecond'",
		"echo café",
		"# not a header",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("search output missing %q:\n%s", want, got)
		}
	}
}

// Without HISTTIMEFORMAT the file is bare command lines, and the note about
// approximate times has to name what bash calls the setting.
func TestImportBashPlainFormat(t *testing.T) {
	useTempDB(t)
	path := writeHistfile(t, "echo one\necho two\necho three\n")

	stdout, _, err := exec(t, "import", "bash", path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(stdout, "imported 3 commands") {
		t.Errorf("stdout = %q, want 3 imported", stdout)
	}
	if !strings.Contains(stdout, "set HISTTIMEFORMAT in bash") {
		t.Errorf("stdout = %q, want bash's own setting named", stdout)
	}

	got, _, err := exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "echo one\necho two\necho three\n"; got != want {
		t.Errorf("search output = %q, want %q", got, want)
	}
}

func TestParseBashHistory(t *testing.T) {
	entries, err := parseBashHistory(strings.NewReader(bashHistfile), formatAuto)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("parsed %d entries, want 4", len(entries))
	}

	if got, want := entries[0].cmd, "echo one"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if got := entries[0].start.Unix(); got != 1700000000 {
		t.Errorf("start = %d, want 1700000000", got)
	}
	if got, want := entries[1].cmd, "echo 'first\nsecond'"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if got, want := entries[3].cmd, "# not a header"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if got := entries[3].start.Unix(); got != 1700000015 {
		t.Errorf("start = %d, want 1700000015", got)
	}
}

// The first line decides the format, so a header turning up later in what
// looked like a plain file is a command line like any other.
func TestParseBashHistoryDetectsFromFirstLine(t *testing.T) {
	entries, err := parseBashHistory(strings.NewReader(
		"echo bare\n#1700000000\necho stamped\n"), formatAuto)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("parsed %d entries, want 3", len(entries))
	}
	if got, want := entries[1].cmd, "#1700000000"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if !entries[1].start.IsZero() {
		t.Errorf("start = %v, want no time in a plain file", entries[1].start)
	}
}

// An extended file has to start with a timestamp, or there is no telling when
// the commands ahead of the first one ran.
func TestParseBashHistoryRejectsUntimedLead(t *testing.T) {
	_, err := parseBashHistory(strings.NewReader(
		"echo bare\n#1700000000\necho stamped\n"), formatExtended)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "no timestamp") {
		t.Errorf("err = %v", err)
	}
}

func TestParseZshHistory(t *testing.T) {
	entries, err := parseZshHistory(strings.NewReader(extendedHistfile), formatAuto)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("parsed %d entries, want 5", len(entries))
	}

	if got, want := entries[0].cmd, "echo one"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if got := entries[0].start.Unix(); got != 1700000000 {
		t.Errorf("start = %d, want 1700000000", got)
	}
	if got, want := entries[1].elapsed, 2; got != want {
		t.Errorf("elapsed = %d, want %d", got, want)
	}
	if got, want := entries[2].cmd, "echo 'first\nsecond'"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if got, want := entries[4].cmd, "echo ': 999:0;not a header'"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
}

// The first line decides the format, so a header turning up later in what
// looked like a plain file is a command line like any other.
func TestParseZshHistoryDetectsFromFirstLine(t *testing.T) {
	entries, err := parseZshHistory(strings.NewReader(
		"echo bare\n: 1700000000:0;echo stamped\n\n"), formatAuto)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(entries))
	}
	if got, want := entries[1].cmd, ": 1700000000:0;echo stamped"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if !entries[1].start.IsZero() {
		t.Errorf("start = %v, want no time in a plain file", entries[1].start)
	}
}

// A header partway through an extended file is a corrupt line, not a command.
func TestParseZshHistoryRejectsStrayLine(t *testing.T) {
	_, err := parseZshHistory(strings.NewReader(
		": 1700000000:0;echo stamped\nnot a header\n"), formatAuto)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "no timestamp") {
		t.Errorf("err = %v", err)
	}
}
