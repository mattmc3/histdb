package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

// at is a local wall clock time, for putting a record at a known moment.
func at(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.Local)
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

// Everything the shell starts inherits the database, so a subshell or a
// script cannot end up recording somewhere else.
func TestInitExportsDatabasePath(t *testing.T) {
	t.Setenv("HISTDB_FILE", "")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg")

	stdout, _, err := exec(t, "init", "zsh")
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	want := ": ${HISTDB_FILE:='/tmp/xdg/histdb/histdb.db'}\nexport HISTDB_FILE\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("snippet missing %q:\n%s", want, stdout)
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
}

// Scope is -s for this session and -S for every session. With neither, the
// zsh wrapper decides by passing -s or not, per SHARE_HISTORY.
func TestSearchSessionScope(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "mine", "--ret", "0",
		"--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, err := exec(t, "record", "--session", "elsewhere", "--cmd", "theirs",
		"--ret", "0", "--start", "200", "--end", "201"); err != nil {
		t.Fatalf("record: %v", err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no flag takes every session", nil, "mine\ntheirs\n"},
		{"-s is this session", []string{"-s"}, "mine\n"},
		{"-S is every session", []string{"-S"}, "mine\ntheirs\n"},
		// The -s is the wrapper's doing, the -S is the caller's.
		{"-S wins over -s", []string{"-s", "-S"}, "mine\ntheirs\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := exec(t, append(tc.args, "--columns", "cmd")...)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
		})
	}
}

// --fail and --success are gone until --where can express them.
func TestSearchHasNoStatusFlags(t *testing.T) {
	useTempDB(t)

	// A database, so a missing one cannot be what fails instead.
	if _, _, err := exec(t, "record", "--cmd", "ls", "--ret", "1",
		"--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}

	for _, arg := range []string{"-f", "--fail", "--success"} {
		if _, _, err := exec(t, arg); err == nil {
			t.Errorf("%s: want error, got nil", arg)
		}
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

// Ranking by frequency, with prefix matching and one column, is what a
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

	t.Run("one column is bare command lines", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--columns", "cmd")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "ls\ngit push\ngit pull\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("listing shows runs and last use", func(t *testing.T) {
		stdout, _, err := exec(t, "-F")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		want := regexp.MustCompile(`^5  \d{4}-\d\d-\d\d \d\d:\d\d  ls\n`)
		if !want.MatchString(stdout) {
			t.Errorf("stdout = %q, want %v", stdout, want)
		}
	})

	t.Run("anchored pattern matches only the start", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--columns", "cmd", "--like", "git p%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git push\ngit pull\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("unanchored pattern matches anywhere", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--columns", "cmd", "--like", "%push%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git push\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("one suggestion", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--columns", "cmd", "-n", "1", "--like", "git%")
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
		stdout, _, err := exec(t, "--columns", "cmd", "--like", "git%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git status\ngit push\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("unanchored", func(t *testing.T) {
		stdout, _, err := exec(t, "--columns", "cmd", "--like", "%git%")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if !strings.Contains(stdout, "legit reason") {
			t.Errorf("stdout = %q, want the unanchored match too", stdout)
		}
	})

	t.Run("underscore is a wildcard", func(t *testing.T) {
		stdout, _, err := exec(t, "--columns", "cmd", "--like", "git s_atus")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "git status\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("a caller can still escape a wildcard", func(t *testing.T) {
		stdout, _, err := exec(t, "--columns", "cmd", "--like", `%50\%%`)
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
		if _, _, err := exec(t, "--columns", "cmd", "--like", attack); err != nil {
			t.Errorf("search %q: %v", attack, err)
		}
	}

	// Everything still there, and the search still works.
	stdout, _, err := exec(t, "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "git status\n"; stdout != want {
		t.Errorf("stdout = %q, want %q: the table should be intact", stdout, want)
	}
}

// The default listing is what `fc -li` prints: id, time, command, no header.
func TestSearchDefaultListing(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "git status", "--ret", "0",
		"--start", "100", "--end", "100.5"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, err := exec(t, "record", "--cmd", "make build", "--ret", "2",
		"--start", "200", "--end", "201"); err != nil {
		t.Fatalf("record: %v", err)
	}

	stdout, _, err := exec(t)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	want := regexp.MustCompile(
		`^1  \d{4}-\d\d-\d\d \d\d:\d\d  git status\n` +
			`2  \d{4}-\d\d-\d\d \d\d:\d\d  make build\n$`)
	if !want.MatchString(stdout) {
		t.Errorf("stdout = %q, want %v", stdout, want)
	}
}

// The star zsh puts on another session's commands under SHARE_HISTORY.
func TestSearchStarsOtherSessions(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "mine", "--ret", "0",
		"--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, err := exec(t, "record", "--session", "elsewhere", "--cmd", "theirs",
		"--ret", "0", "--start", "200", "--end", "201"); err != nil {
		t.Fatalf("record: %v", err)
	}

	stdout, _, err := exec(t)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, want := range []string{"1  ", "2* "} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want a row starting %q", stdout, want)
		}
	}
}

func TestSearchColumns(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "make build", "--ret", "2",
		"--cwd", "/tmp", "--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}

	t.Run("one column is bare command lines", func(t *testing.T) {
		stdout, _, err := exec(t, "--columns", "cmd")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "make build\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("order is the order asked for", func(t *testing.T) {
		stdout, _, err := exec(t, "--columns", "ret,dur,cwd,cmd")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := "2  1.00  /tmp  make build\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("unknown column names the ones that work", func(t *testing.T) {
		_, _, err := exec(t, "--columns", "cmd,nope")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), `unknown column "nope"`) {
			t.Errorf("err = %v", err)
		}
	})
}

// One JSON object per line, so a command with a newline in it stays one
// record and the whole listing streams.
func TestSearchJSONL(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "git status", "--cwd", "/tmp",
		"--ret", "0", "--start", "100", "--end", "100.5"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, err := exec(t, "record", "--cmd", "echo 'first\nsecond'", "--cwd", "/tmp",
		"--ret", "2", "--start", "200", "--end", "201"); err != nil {
		t.Fatalf("record: %v", err)
	}

	stdout, _, err := exec(t, "--jsonl")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one per command:\n%s", len(lines), stdout)
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 1 is not JSON: %v", err)
	}
	want := map[string]any{
		"id": 1.0, "dur": 0.5, "ret": 0.0, "cwd": "/tmp",
		"session": "test-session", "cmd": "git status",
	}
	for key, value := range want {
		if first[key] != value {
			t.Errorf("%s = %#v, want %#v", key, first[key], value)
		}
	}
	if _, err := time.Parse(time.RFC3339, first["time"].(string)); err != nil {
		t.Errorf("time = %v, want RFC3339: %v", first["time"], err)
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("line 2 is not JSON: %v", err)
	}
	if got, want := second["cmd"], "echo 'first\nsecond'"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
}

// A command still running has no duration and no status, which is null and
// not a dash once it is JSON.
func TestSearchJSONLNullsWhatIsUnknown(t *testing.T) {
	useTempDB(t)

	// Another session: the one command this session never sees is the one it
	// is running right now.
	if _, _, err := exec(t, "record", "--session", "elsewhere",
		"--cmd", "sleep 100", "--start", "100"); err != nil {
		t.Fatalf("record: %v", err)
	}

	stdout, _, err := exec(t, "--jsonl")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &row); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	for _, key := range []string{"dur", "ret"} {
		if value, ok := row[key]; !ok || value != nil {
			t.Errorf("%s = %#v, want null", key, value)
		}
	}
}

func TestSearchJSONLColumns(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "git status", "--ret", "0",
		"--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}

	t.Run("only what was asked for", func(t *testing.T) {
		stdout, _, err := exec(t, "--jsonl", "--columns", "cmd,ret")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := `{"cmd":"git status","ret":0}` + "\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("ranking has its own columns", func(t *testing.T) {
		stdout, _, err := exec(t, "-F", "--jsonl", "--columns", "runs,cmd")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if want := `{"runs":1,"cmd":"git status"}` + "\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})
}

// What the session knows belongs to every command that ran in it.
func TestSearchSessionColumns(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--shell", "zsh", "--session", "s1",
		"--tty", "ttys009", "--host", "box", "--user", "someone",
		"--cmd", "git status", "--ret", "0", "--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}

	stdout, _, err := exec(t, "--jsonl")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &row); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	want := map[string]any{
		"session": "s1", "shell": "zsh", "host": "box",
		"user": "someone", "tty": "ttys009",
	}
	for key, value := range want {
		if row[key] != value {
			t.Errorf("%s = %#v, want %#v", key, row[key], value)
		}
	}

	table, _, err := exec(t, "--columns", "host,user,cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "box  someone  git status\n"; table != want {
		t.Errorf("table = %q, want %q", table, want)
	}
}

// -n 0 is every match. JSON is read by a program, so it takes every match
// unless told otherwise.
func TestSearchLimit(t *testing.T) {
	useTempDB(t)

	for i := range 25 {
		if _, _, err := exec(t, "record", "--cmd", fmt.Sprintf("cmd%02d", i),
			"--ret", "0", "--start", fmt.Sprint(100+i), "--end", fmt.Sprint(101+i)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	cases := []struct {
		name       string
		args       []string
		wantRows   int
		wantStderr string
	}{
		{"table stops at the default",
			[]string{"--columns", "cmd"}, 20,
			"(limit: 20 rows; change with -n <N>)\n"},
		{"-n 0 is everything",
			[]string{"-n", "0", "--columns", "cmd"}, 25, ""},
		{"-n counts",
			[]string{"-n", "5", "--columns", "cmd"}, 5, ""},
		{"jsonl is everything",
			[]string{"--jsonl"}, 25, ""},
		{"jsonl still takes -n",
			[]string{"--jsonl", "-n", "5"}, 5, ""},
		{"ranking warns at the default",
			[]string{"-F", "--columns", "cmd"}, 20,
			"(limit: 20 rows; change with -n <N>)\n"},
		{"ranking is quiet with -n",
			[]string{"-F", "-n", "5", "--columns", "cmd"}, 5, ""},
		{"ranking takes -n 0",
			[]string{"-F", "-n", "0", "--columns", "cmd"}, 25, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := exec(t, tc.args...)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if got := strings.Count(stdout, "\n"); got != tc.wantRows {
				t.Errorf("got %d rows, want %d", got, tc.wantRows)
			}
			if stderr != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr, tc.wantStderr)
			}
		})
	}
}

// A range runs midnight to midnight when neither end names a time, so one
// date on both sides is that whole day.
func TestSearchSinceUntil(t *testing.T) {
	useTempDB(t)

	moments := []struct {
		cmd string
		at  time.Time
	}{
		{"before", at(2026, 1, 14, 12, 0)},
		{"morning", at(2026, 1, 15, 9, 0)},
		{"late", at(2026, 1, 15, 23, 30)},
		{"after", at(2026, 1, 16, 8, 0)},
	}
	for _, m := range moments {
		start := fmt.Sprint(m.at.Unix())
		if _, _, err := exec(t, "record", "--cmd", m.cmd, "--ret", "0",
			"--start", start, "--end", start); err != nil {
			t.Fatalf("record %q: %v", m.cmd, err)
		}
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"one date is that whole day",
			[]string{"--since", "2026-01-15", "--until", "2026-01-15"}, "morning\nlate\n"},
		{"since is inclusive from midnight",
			[]string{"--since", "2026-01-15"}, "morning\nlate\nafter\n"},
		{"until covers the day it names",
			[]string{"--until", "2026-01-15"}, "before\nmorning\nlate\n"},
		{"a time of day is taken as given",
			[]string{"--since", "2026-01-15 10:00"}, "late\nafter\n"},
		{"a span covers both days named",
			[]string{"--since", "2026-01-14", "--until", "2026-01-15"}, "before\nmorning\nlate\n"},
		{"the far end includes the day it names",
			[]string{"--since", "2026-01-14", "--until", "2026-01-16"}, "before\nmorning\nlate\nafter\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := exec(t, append(tc.args, "--columns", "cmd")...)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
		})
	}
}

func TestSearchSinceUntilErrors(t *testing.T) {
	useTempDB(t)

	if _, _, err := exec(t, "record", "--cmd", "ls", "--ret", "0",
		"--start", "100", "--end", "101"); err != nil {
		t.Fatalf("record: %v", err)
	}

	t.Run("unreadable time names the flag", func(t *testing.T) {
		_, _, err := exec(t, "--since", "tea time")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "--since") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a backwards range is a mistake, not an empty answer", func(t *testing.T) {
		_, _, err := exec(t, "--since", "2026-01-16", "--until", "2026-01-14")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "--since") || !strings.Contains(err.Error(), "--until") {
			t.Errorf("err = %v", err)
		}
	})
}

// Ranking shares the filter, so it takes a range too.
func TestSearchSinceWithFrequency(t *testing.T) {
	useTempDB(t)

	for _, m := range []struct {
		cmd string
		at  time.Time
	}{
		{"old", at(2026, 1, 1, 9, 0)},
		{"new", at(2026, 1, 20, 9, 0)},
	} {
		start := fmt.Sprint(m.at.Unix())
		if _, _, err := exec(t, "record", "--cmd", m.cmd, "--ret", "0",
			"--start", start, "--end", start); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	stdout, _, err := exec(t, "-F", "--since", "2026-01-15", "--columns", "cmd")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := "new\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// Exit status survives in the listing as the color of the id.
func TestListingColorsIDByStatus(t *testing.T) {
	entries := []history.Entry{
		{ID: 1, Cmd: "ok", Ret: 0, StartAt: time.Unix(100, 0), EndAt: time.Unix(101, 0)},
		{ID: 2, Cmd: "bad", Ret: 1, StartAt: time.Unix(200, 0), EndAt: time.Unix(201, 0)},
		{ID: 3, Cmd: "running", StartAt: time.Unix(300, 0)},
	}

	var buf bytes.Buffer
	cols, err := entryColumns("id,cmd")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if err := renderEntries(&buf, entries, cols, "", true); err != nil {
		t.Fatalf("render: %v", err)
	}

	want := "\x1b[32m1\x1b[0m  ok\n\x1b[31m2\x1b[0m  bad\n3  running\n"
	if got := buf.String(); got != want {
		t.Errorf("rendered %q, want %q", got, want)
	}
}

// The ret column carries the same status the id color does, so it is colored
// the same way.
func TestListingColorsRetByStatus(t *testing.T) {
	entries := []history.Entry{
		{ID: 1, Cmd: "ok", Ret: 0, StartAt: time.Unix(100, 0), EndAt: time.Unix(101, 0)},
		{ID: 2, Cmd: "bad", Ret: 1, StartAt: time.Unix(200, 0), EndAt: time.Unix(201, 0)},
		{ID: 3, Cmd: "running", StartAt: time.Unix(300, 0)},
	}

	var buf bytes.Buffer
	cols, err := entryColumns("id,ret,cmd")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if err := renderEntries(&buf, entries, cols, "", true); err != nil {
		t.Fatalf("render: %v", err)
	}

	want := "\x1b[32m1\x1b[0m  \x1b[32m0\x1b[0m  ok\n" +
		"\x1b[31m2\x1b[0m  \x1b[31m1\x1b[0m  bad\n" +
		"3  -  running\n"
	if got := buf.String(); got != want {
		t.Errorf("rendered %q, want %q", got, want)
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

func TestSessionFlagNeedsSession(t *testing.T) {
	useTempDB(t)
	t.Setenv("HISTDB_SESSION", "")

	_, _, err := exec(t, "-s")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "HISTDB_SESSION is not set") {
		t.Errorf("err = %v", err)
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
