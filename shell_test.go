package main

// Harness for the shell integration tests, which drive a real shell so the
// hooks in shell/ are what run, not a Go stand-in.

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mattmc3/histdb/internal/history"
)

// binDir holds a built histdb for the hooks to call, or "" when no shell that
// could run them is installed.
var binDir string

// shells maps a shell to how a test drives it: the arguments that start an
// interactive one with none of its own startup files, and the line that loads
// the integration.
var shells = map[string]struct {
	args []string
	load string
}{
	"zsh":  {[]string{"-f", "-is"}, "source <(histdb init zsh)"},
	"bash": {[]string{"--norc", "--noprofile", "-is"}, `eval "$(histdb init bash)"`},
}

// shellPath holds the binary each shell's tests drive, filled in by TestMain
// and empty for a shell this machine cannot run them on.
var shellPath = map[string]string{}

func TestMain(m *testing.M) {
	for name := range shells {
		shellPath[name] = findShell(name)
	}
	if anyShellInstalled() {
		dir, err := os.MkdirTemp("", "histdb-bin")
		if err != nil {
			fmt.Fprintln(os.Stderr, "temp dir:", err)
			os.Exit(1)
		}
		out, err := osexec.Command("go", "build", "-o", filepath.Join(dir, "histdb"), ".").CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "build histdb: %v\n%s", err, out)
			os.RemoveAll(dir)
			os.Exit(1)
		}
		binDir = dir
		defer os.RemoveAll(dir)
	}
	os.Exit(m.Run())
}

// findShell picks the binary to drive, or "" when there is none to drive. The
// bash integration needs 5.0, and macOS still ships 3.2 as the `bash` on PATH,
// so the ones a newer bash is usually installed as are tried after it.
func findShell(name string) string {
	candidates := []string{name}
	if name == "bash" {
		candidates = append(candidates, "/opt/homebrew/bin/bash", "/usr/local/bin/bash")
	}

	for _, candidate := range candidates {
		path, err := osexec.LookPath(candidate)
		if err != nil {
			continue
		}
		if name != "bash" {
			return path
		}
		out, err := osexec.Command(path, "-c", "echo ${BASH_VERSINFO[0]}").Output()
		if err != nil {
			continue
		}
		if major, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && major >= 5 {
			return path
		}
	}
	return ""
}

func anyShellInstalled() bool {
	for _, path := range shellPath {
		if path != "" {
			return true
		}
	}
	return false
}

// settleCmd gives the disowned record writes time to land before a query in
// the same shell reads them back.
const settleCmd = "sleep 1\n"

type shellSession struct {
	t      *testing.T
	shell  string
	dir    string
	db     string
	stderr string
}

func newZshShell(t *testing.T) *shellSession { return newShell(t, "zsh") }

func newBashShell(t *testing.T) *shellSession { return newShell(t, "bash") }

func newShell(t *testing.T, shell string) *shellSession {
	t.Helper()

	if shellPath[shell] == "" || binDir == "" {
		missing := shell + " not installed"
		if shell == "bash" {
			missing = "no bash 5.0 or newer installed"
		}
		// CI installs every shell deliberately, and `go test` says nothing about
		// a skip, so there a missing one means the coverage quietly went away.
		if os.Getenv("CI") != "" {
			t.Fatal(missing)
		}
		t.Skip(missing)
	}
	// Almost all of a shell test is spent waiting on a shell that is not this
	// process. Each one owns its database and its temporary directory, so they
	// have nothing to wait on each other for.
	t.Parallel()

	dir := t.TempDir()
	return &shellSession{t: t, shell: shell, dir: dir, db: filepath.Join(dir, "histdb.db")}
}

// run starts a shell with setup already in effect before the hooks are
// installed, so those lines are not themselves recorded, then feeds it script.
// Returns everything the shell printed.
func (s *shellSession) run(setup, script string) string {
	s.t.Helper()

	input := setup + "\n" + shells[s.shell].load + "\n" + script + "\n"
	cmd := osexec.Command(shellPath[s.shell], shells[s.shell].args...)
	cmd.Stdin = strings.NewReader(input)
	// An interactive shell opens the controlling terminal to set up job control,
	// and touching it from a background process group stops the process, which
	// the terminal reports as `suspended (tty input)` against the whole test
	// run. Its own session leaves it no terminal to reach for.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(),
		"HISTDB_FILE="+s.db,
		// Bash keeps a history list of its own and the hooks read it back, so
		// point it somewhere that is not the caller's.
		"HISTFILE="+filepath.Join(s.dir, "shell_history"),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	// Kept apart from stdout, which a test reads back: a search writes its
	// row-limit notice to stderr, and so does anything that went wrong.
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		s.t.Fatalf("%s: %v: %s", s.shell, err, errs.String())
	}
	s.stderr = errs.String()
	s.settle()
	return out.String()
}

// settle waits for the backgrounded record writes to stop arriving. They are
// disowned, so the shell exiting says nothing about whether they landed.
func (s *shellSession) settle() {
	s.t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	last, stable := -1, 0
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		n := len(s.recorded())
		if n == last {
			if stable++; stable >= 4 {
				return
			}
			continue
		}
		last, stable = n, 0
	}
	s.t.Fatal("timed out waiting for records to land")
}

// historyFile is what the shell left in its own history file. histdb records
// every command, and hands the settings that filter this file back to it, so
// the two lists are compared separately.
func (s *shellSession) historyFile() []string {
	s.t.Helper()

	text, err := os.ReadFile(filepath.Join(s.dir, "shell_history"))
	if err != nil {
		s.t.Fatalf("read history file: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(text), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// count is how many of lines are exactly want.
func count(lines []string, want string) int {
	n := 0
	for _, line := range lines {
		if line == want {
			n++
		}
	}
	return n
}

// seed writes a row as if another shell had run it.
func (s *shellSession) seed(session, cmd string, start float64) {
	s.t.Helper()

	seed := osexec.Command(filepath.Join(binDir, "histdb"), "record",
		"--shell", s.shell, "--session", session, "--cmd", cmd, "--ret", "0",
		"--start", fmt.Sprint(start), "--end", fmt.Sprint(start+1),
	)
	// Never inherit the environment here: without this the row lands in the
	// caller's own history database.
	seed.Env = append(os.Environ(), "HISTDB_FILE="+s.db)
	if out, err := seed.CombinedOutput(); err != nil {
		s.t.Fatalf("seed %q: %v: %s", cmd, err, out)
	}
}

func (s *shellSession) recorded() []string {
	s.t.Helper()

	if _, err := os.Stat(s.db); err != nil {
		return nil
	}
	store, err := history.Open(context.Background(), s.db)
	if err != nil {
		s.t.Fatalf("open: %v", err)
	}
	defer store.Close()

	entries, err := store.Entries().Search(context.Background(), history.Filter{Limit: 1000})
	if err != nil {
		s.t.Fatalf("search: %v", err)
	}
	cmds := make([]string, len(entries))
	for i, e := range entries {
		cmds[i] = e.Cmd
	}
	return cmds
}

// pipeStatus returns what was recorded for cmd's pipeline.
func (s *shellSession) pipeStatus(cmd string) string {
	s.t.Helper()

	store, err := history.Open(context.Background(), s.db)
	if err != nil {
		s.t.Fatalf("open: %v", err)
	}
	defer store.Close()

	entries, err := store.Entries().Search(context.Background(), history.Filter{Limit: 1000})
	if err != nil {
		s.t.Fatalf("search: %v", err)
	}
	for _, e := range entries {
		if e.Cmd == cmd {
			return e.PipeStatus
		}
	}
	s.t.Fatalf("no row for %q", cmd)
	return ""
}

func (s *shellSession) assertRecorded(want ...string) {
	s.t.Helper()

	got := s.recorded()
	if len(got) != len(want) {
		s.t.Fatalf("recorded %q, want %q", got, want)
	}
	for _, w := range want {
		if !contains(got, w) {
			s.t.Errorf("recorded %q, missing %q", got, w)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
