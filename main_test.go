package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattmc3/histdb/internal/history"
)

// exec runs the CLI against a database private to the test.
func exec(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func useTempDB(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "histdb.db")
	t.Setenv("HISTDB_FILE", path)
	t.Setenv("HISTDB_SESSION", "test-session")
	return path
}

func TestVersionFlags(t *testing.T) {
	for _, arg := range []string{"-v", "--version"} {
		stdout, _, err := exec(t, arg)
		if err != nil {
			t.Fatalf("run %s: %v", arg, err)
		}
		if got, want := stdout, "histdb 0.0.1\n"; got != want {
			t.Errorf("%s stdout = %q, want %q", arg, got, want)
		}
	}
}

func TestHelpFlags(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		stdout, _, err := exec(t, arg)
		if err != nil {
			t.Fatalf("run %s: %v", arg, err)
		}
		if !strings.Contains(stdout, "histdb init <shell>") {
			t.Errorf("%s help missing init usage: %q", arg, stdout)
		}
	}
}

// getopt(3) syntax: -v is short, --version is long, -version is neither.
func TestRejectsSingleDashLongFlag(t *testing.T) {
	stdout, _, err := exec(t, "-version")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestCombinedShortFlags(t *testing.T) {
	stdout, _, err := exec(t, "-hv")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout, "usage:") {
		t.Errorf("stdout = %q, want help", stdout)
	}
}

func TestUnknownFlag(t *testing.T) {
	_, stderr, err := exec(t, "-x")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("stderr missing usage: %q", stderr)
	}
}

func TestInitZsh(t *testing.T) {
	stdout, _, err := exec(t, "init", "zsh")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(stdout, "# histdb 0.0.1 init for zsh\n") {
		t.Errorf("missing header: %q", stdout)
	}
	for _, want := range []string{"_histdb_preexec", "_histdb_precmd", `"$HISTDB_BIN" record`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("snippet missing %q", want)
		}
	}
}

// init names the binary that printed the snippet, rather than leave it to PATH.
func TestInitPinsBinaryPath(t *testing.T) {
	stdout, _, err := exec(t, "init", "zsh")
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	self, err := os.Executable()
	if err != nil {
		t.Skip("no executable path on this platform")
	}
	if want := ": ${HISTDB_BIN:='" + self + "'}\n"; !strings.Contains(stdout, want) {
		t.Errorf("snippet missing %q", want)
	}
	if strings.Contains(stdout, "command histdb ") {
		t.Error("snippet still searches PATH for histdb")
	}
}

func TestInitUnsupportedShell(t *testing.T) {
	_, _, err := exec(t, "init", "tcsh")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), `unsupported shell "tcsh"`) {
		t.Errorf("err = %v", err)
	}
}

func TestInitRequiresShellArg(t *testing.T) {
	if _, _, err := exec(t, "init"); err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestSearchWithoutDatabase(t *testing.T) {
	path := useTempDB(t)

	_, _, err := exec(t)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "no database at "+path) {
		t.Errorf("err = %v, want no database at %s", err, path)
	}
}

func TestRecordThenSearch(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--shell", "zsh", "--cmd", "git status",
		"--ret", "0", "--start", "100", "--end", "100.5", "--cwd", "/tmp"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, err := exec(t, "record", "--shell", "zsh", "--cmd", "make build",
		"--ret", "2", "--start", "200", "--end", "201", "--cwd", "/tmp"); err != nil {
		t.Fatalf("record: %v", err)
	}

	stdout, _, err := exec(t)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(stdout, "git status") || !strings.Contains(stdout, "make build") {
		t.Fatalf("search output = %q, want both commands", stdout)
	}
	if !strings.HasPrefix(stdout, "time") {
		t.Errorf("missing header row: %q", stdout)
	}

	stdout, _, err = exec(t, "--fail")
	if err != nil {
		t.Fatalf("search --fail: %v", err)
	}
	if strings.Contains(stdout, "git status") {
		t.Errorf("--fail returned a successful command: %q", stdout)
	}
	if !strings.Contains(stdout, "make build") {
		t.Errorf("--fail missing failed command: %q", stdout)
	}
}

// Matching is always an explicit --like, so a bare word is a mistake and says
// what to type instead.
func TestSearchRejectsBareWord(t *testing.T) {
	useTempDB(t)

	_, _, err := exec(t, "git")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), `unexpected argument "git"`) ||
		!strings.Contains(err.Error(), "--like") {
		t.Errorf("err = %v, want it to point at --like", err)
	}
}

func TestSearchNoDups(t *testing.T) {
	useTempDB(t)

	for i, cmd := range []string{"ls", "git status", "ls"} {
		start := fmt.Sprint(100 + i)
		end := fmt.Sprint(101 + i)
		if _, _, err := exec(t, "record", "--cmd", cmd, "--ret", "0", "--start", start, "--end", end); err != nil {
			t.Fatalf("record %q: %v", cmd, err)
		}
	}

	stdout, _, err := exec(t, "--no-dups")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := strings.Count(stdout, "ls"); got != 1 {
		t.Errorf("ls appears %d times, want 1: %q", got, stdout)
	}
	if !strings.Contains(stdout, "git status") {
		t.Errorf("output missing git status: %q", stdout)
	}
}

// -r widens -d to the checkout the directory belongs to.
func TestSearchRepoFlag(t *testing.T) {
	useTempDB(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	records := []struct{ cmd, cwd string }{
		{"in sub", sub},
		{"at root", repo},
		{"unrelated", t.TempDir()},
	}
	for i, r := range records {
		if _, _, err := exec(t, "record", "--cmd", r.cmd, "--cwd", r.cwd, "--ret", "0",
			"--start", fmt.Sprint(100+i), "--end", fmt.Sprint(101+i)); err != nil {
			t.Fatalf("record %q: %v", r.cmd, err)
		}
	}

	t.Chdir(sub)

	stdout, _, err := exec(t, "-d")
	if err != nil {
		t.Fatalf("search -d: %v", err)
	}
	if !strings.Contains(stdout, "in sub") || strings.Contains(stdout, "at root") {
		t.Errorf("-d output = %q, want only the subdirectory", stdout)
	}

	stdout, _, err = exec(t, "-r")
	if err != nil {
		t.Fatalf("search -r: %v", err)
	}
	if !strings.Contains(stdout, "in sub") || !strings.Contains(stdout, "at root") {
		t.Errorf("-r output = %q, want the whole repository", stdout)
	}
	if strings.Contains(stdout, "unrelated") {
		t.Errorf("-r leaked another directory: %q", stdout)
	}
}

// Outside a checkout there is no wider scope, so -r is just -d.
func TestSearchRepoFlagOutsideRepo(t *testing.T) {
	useTempDB(t)
	loose := t.TempDir()
	other := t.TempDir()

	for i, r := range []struct{ cmd, cwd string }{{"right here", loose}, {"somewhere else", other}} {
		if _, _, err := exec(t, "record", "--cmd", r.cmd, "--cwd", r.cwd, "--ret", "0",
			"--start", fmt.Sprint(100+i), "--end", fmt.Sprint(101+i)); err != nil {
			t.Fatalf("record %q: %v", r.cmd, err)
		}
	}

	t.Chdir(loose)

	stdout, _, err := exec(t, "-r")
	if err != nil {
		t.Fatalf("search -r: %v", err)
	}
	if !strings.Contains(stdout, "right here") {
		t.Errorf("-r output = %q, want this directory's command", stdout)
	}
	if strings.Contains(stdout, "somewhere else") {
		t.Errorf("-r outside a repo leaked another directory: %q", stdout)
	}
}

// Only the start write knows where a command ran. When the finish write wins
// the race and inserts first, its own working directory must not end up as the
// command's.
func TestFinishDoesNotInventCwd(t *testing.T) {
	useTempDB(t)
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	// Finish first, as happens when the start write is still starting up.
	if _, _, err := exec(t, "record", "--cmd", "ls", "--start", "100",
		"--end", "101", "--ret", "0"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, _, err := exec(t, "record", "--cmd", "ls", "--cwd", "/tmp", "--start", "100"); err != nil {
		t.Fatalf("start: %v", err)
	}

	store, err := history.Open(context.Background(), dbPath())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	entries, err := store.Entries().Search(context.Background(), history.Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if got := entries[0].Cwd; got != "/tmp" {
		t.Errorf("cwd = %q, want /tmp from the start write", got)
	}
	if !entries[0].Finished() {
		t.Error("outcome lost")
	}
}

// Ranking by frequency, with prefix matching and plain output, is what a
// suggestion strategy needs.
func TestSortByFrequency(t *testing.T) {
	useTempDB(t)

	start := 0
	for cmd, runs := range map[string]int{"git push": 3, "git pull": 1, "ls": 5} {
		for range runs {
			start++
			if _, _, err := exec(t, "record", "--cmd", cmd, "--ret", "0",
				"--start", fmt.Sprint(100+start), "--end", fmt.Sprint(101+start)); err != nil {
				t.Fatalf("record %q: %v", cmd, err)
			}
		}
	}

	t.Run("plain output is bare command lines", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--plain")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "ls\ngit push\ngit pull\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("table shows runs and last use", func(t *testing.T) {
		stdout, _, err := exec(t, "-F")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if !strings.HasPrefix(stdout, "runs") {
			t.Errorf("stdout = %q, want a runs column", stdout)
		}
		if !strings.Contains(stdout, "5") || !strings.Contains(stdout, "ls") {
			t.Errorf("stdout = %q, want the count beside the command", stdout)
		}
	})

	t.Run("anchored pattern matches only the start", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--plain", "--like", "git p%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git push\ngit pull\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("unanchored pattern matches anywhere", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--plain", "--like", "%push%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git push\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("one suggestion", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--plain", "-n", "1", "--like", "git%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git push\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("head conflicts", func(t *testing.T) {
		_, _, err := exec(t, "-F", "-H")
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("err = %v, want a conflict", err)
		}
	})

	t.Run("prefer-here needs frequency", func(t *testing.T) {
		_, _, err := exec(t, "--prefer-here")
		if err == nil || !strings.Contains(err.Error(), "needs --sort-by-frequency") {
			t.Errorf("err = %v, want a conflict", err)
		}
	})
}

// --like hands the pattern to SQL as written, so % and _ are wildcards.
func TestSearchLike(t *testing.T) {
	useTempDB(t)

	for i, cmd := range []string{"git status", "git push", "legit reason", "echo 50% off"} {
		if _, _, err := exec(t, "record", "--cmd", cmd, "--ret", "0",
			"--start", fmt.Sprint(100+i), "--end", fmt.Sprint(101+i)); err != nil {
			t.Fatalf("record %q: %v", cmd, err)
		}
	}

	t.Run("anchored", func(t *testing.T) {
		stdout, _, err := exec(t, "--plain", "--like", "git%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git status\ngit push\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("unanchored", func(t *testing.T) {
		stdout, _, err := exec(t, "--plain", "--like", "%git%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if !strings.Contains(stdout, "legit reason") {
			t.Errorf("stdout = %q, want the unanchored match too", stdout)
		}
	})

	t.Run("underscore is a wildcard", func(t *testing.T) {
		stdout, _, err := exec(t, "--plain", "--like", "git s_atus")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git status\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("a caller can still escape a wildcard", func(t *testing.T) {
		stdout, _, err := exec(t, "--plain", "--like", `%50\%%`)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "echo 50% off\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("a stray argument is rejected", func(t *testing.T) {
		if _, _, err := exec(t, "--like", "git%", "git"); err == nil {
			t.Error("want error, got nil")
		}
	})
}

// The pattern is a bound parameter, so SQL in it is only ever text to match.
func TestSearchLikeIsNotInjectable(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "git status", "--ret", "0",
		"--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}

	for _, attack := range []string{
		`'; DROP TABLE history; --`,
		`%'; DELETE FROM history WHERE 1=1; --`,
		`' UNION SELECT 1,2,3,4,5,6,7,8,9,10,11,12,13,14,15 --`,
	} {
		if _, _, err := exec(t, "--plain", "--like", attack); err != nil {
			t.Errorf("search %q: %v", attack, err)
		}
	}

	// Everything still there, and the search still works.
	stdout, _, err := exec(t, "--plain")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "git status\n"; stdout != want {
		t.Errorf("stdout = %q, want %q: the table should be intact", stdout, want)
	}
}

// --plain drops the table for a plain search too.
func TestSearchPlain(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "git status", "--ret", "0",
		"--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}

	stdout, _, err := exec(t, "--plain")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "git status\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// An end time without a status would otherwise store the "still running"
// sentinel as a real exit code.
func TestRecordRejectsEndWithoutRet(t *testing.T) {
	useTempDB(t)

	_, _, err := exec(t, "record", "--cmd", "ls", "--start", "100", "--end", "101")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "--ret is required with --end") {
		t.Errorf("err = %v", err)
	}
}

func TestRecordRequiresCmd(t *testing.T) {
	useTempDB(t)

	_, _, err := exec(t, "record", "--ret", "0", "--start", "1")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "--cmd is required") {
		t.Errorf("err = %v", err)
	}
}

func TestFailAndSuccessConflict(t *testing.T) {
	useTempDB(t)

	_, _, err := exec(t, "-f", "-s")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v", err)
	}
}

func TestSessionFlagNeedsSession(t *testing.T) {
	useTempDB(t)
	t.Setenv("HISTDB_SESSION", "")

	_, _, err := exec(t, "-S")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "HISTDB_SESSION is not set") {
		t.Errorf("err = %v", err)
	}
}

func TestSessionFilter(t *testing.T) {
	useTempDB(t)
	t.Setenv("HISTDB_SESSION", "session-a")

	if _, _, err := exec(t, "record", "--cmd", "mine", "--session", "session-a",
		"--ret", "0", "--start", "1", "--end", "2"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, err := exec(t, "record", "--cmd", "theirs", "--session", "session-b",
		"--ret", "0", "--start", "3", "--end", "4"); err != nil {
		t.Fatalf("record: %v", err)
	}

	stdout, _, err := exec(t, "-S")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(stdout, "mine") || strings.Contains(stdout, "theirs") {
		t.Errorf("output = %q, want only this session", stdout)
	}
}

func TestDBPath(t *testing.T) {
	t.Run("HISTDB_FILE wins", func(t *testing.T) {
		t.Setenv("HISTDB_FILE", "/tmp/explicit.db")
		t.Setenv("XDG_DATA_HOME", "/tmp/xdg")
		if got, want := dbPath(), "/tmp/explicit.db"; got != want {
			t.Errorf("dbPath = %q, want %q", got, want)
		}
	})

	t.Run("XDG default", func(t *testing.T) {
		t.Setenv("HISTDB_FILE", "")
		t.Setenv("XDG_DATA_HOME", "/tmp/xdg")
		if got, want := dbPath(), "/tmp/xdg/histdb/histdb.db"; got != want {
			t.Errorf("dbPath = %q, want %q", got, want)
		}
	})
}
