package main

// Harness for the shell integration tests, which drive a real shell so the
// hooks in shell/ are what run, not a Go stand-in.

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
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

func TestMain(m *testing.M) {
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

func anyShellInstalled() bool {
	for name := range shells {
		if _, err := osexec.LookPath(name); err == nil {
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

	if _, err := osexec.LookPath(shell); err != nil || binDir == "" {
		t.Skip(shell + " not installed")
	}
	dir := t.TempDir()
	return &shellSession{t: t, shell: shell, dir: dir, db: filepath.Join(dir, "histdb.db")}
}

// run starts a shell with setup already in effect before the hooks are
// installed, so those lines are not themselves recorded, then feeds it script.
// Returns everything the shell printed.
func (s *shellSession) run(setup, script string) string {
	s.t.Helper()

	input := setup + "\n" + shells[s.shell].load + "\n" + script + "\n"
	cmd := osexec.Command(s.shell, shells[s.shell].args...)
	cmd.Stdin = strings.NewReader(input)
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
