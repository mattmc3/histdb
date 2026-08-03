package main

// Tests for the zsh history options histdb honors, driven through a real zsh
// so the hooks and wrapper in shell/zsh.zsh are what run, not a Go stand-in.

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattmc3/histdb/internal/history"
)

// binDir holds a built histdb for the hooks to call, or "" when zsh is absent.
var binDir string

func TestMain(m *testing.M) {
	if _, err := osexec.LookPath("zsh"); err == nil {
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

// settleCmd gives the disowned record writes time to land before a query in
// the same shell reads them back.
const settleCmd = "sleep 1\n"

type zshShell struct {
	t  *testing.T
	db string
}

func newZshShell(t *testing.T) *zshShell {
	t.Helper()

	if binDir == "" {
		t.Skip("zsh not installed")
	}
	return &zshShell{t: t, db: filepath.Join(t.TempDir(), "histdb.db")}
}

// run starts a zsh with setopts already in effect before the hooks are
// installed, so those setopt lines are not themselves recorded, then feeds it
// script. Returns everything the shell printed.
func (z *zshShell) run(setopts, script string) string {
	z.t.Helper()

	input := setopts + "\nsource <(histdb init zsh)\n" + script + "\n"
	cmd := osexec.Command("zsh", "-f", "-is")
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(),
		"HISTDB_FILE="+z.db,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		z.t.Fatalf("zsh: %v", err)
	}
	z.settle()
	return out.String()
}

// settle waits for the backgrounded record writes to stop arriving. They are
// disowned, so the shell exiting says nothing about whether they landed.
func (z *zshShell) settle() {
	z.t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	last, stable := -1, 0
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		n := len(z.recorded())
		if n == last {
			if stable++; stable >= 4 {
				return
			}
			continue
		}
		last, stable = n, 0
	}
	z.t.Fatal("timed out waiting for records to land")
}

// seed writes a row as if another shell had run it.
func (z *zshShell) seed(session, cmd string, start float64) {
	z.t.Helper()

	seed := osexec.Command(filepath.Join(binDir, "histdb"), "record",
		"--shell", "zsh", "--session", session, "--cmd", cmd, "--ret", "0",
		"--start", fmt.Sprint(start), "--end", fmt.Sprint(start+1),
	)
	// Never inherit the environment here: without this the row lands in the
	// caller's own history database.
	seed.Env = append(os.Environ(), "HISTDB_FILE="+z.db)
	if out, err := seed.CombinedOutput(); err != nil {
		z.t.Fatalf("seed %q: %v: %s", cmd, err, out)
	}
}

func (z *zshShell) recorded() []string {
	z.t.Helper()

	if _, err := os.Stat(z.db); err != nil {
		return nil
	}
	store, err := history.Open(context.Background(), z.db)
	if err != nil {
		z.t.Fatalf("open: %v", err)
	}
	defer store.Close()

	entries, err := store.Entries().Search(context.Background(), history.Filter{Limit: 1000})
	if err != nil {
		z.t.Fatalf("search: %v", err)
	}
	cmds := make([]string, len(entries))
	for i, e := range entries {
		cmds[i] = e.Cmd
	}
	return cmds
}

func (z *zshShell) assertRecorded(want ...string) {
	z.t.Helper()

	got := z.recorded()
	if len(got) != len(want) {
		z.t.Fatalf("recorded %q, want %q", got, want)
	}
	for _, w := range want {
		if !contains(got, w) {
			z.t.Errorf("recorded %q, missing %q", got, w)
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

// A version manager or a script can rewrite PATH after the hooks install.
func TestZshRecordsAfterPathLosesHistdb(t *testing.T) {
	z := newZshShell(t)
	z.run("", "PATH=/nonexistent\necho offpath")

	z.assertRecorded("PATH=/nonexistent", "echo offpath")
}

// HIST_IGNORE_SPACE: a command whose first character is a space is not
// recorded.
func TestZshIgnoreSpace(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		z := newZshShell(t)
		z.run("setopt hist_ignore_space", " echo secret\necho kept")

		z.assertRecorded("echo kept")
	})

	t.Run("off", func(t *testing.T) {
		z := newZshShell(t)
		z.run("unsetopt hist_ignore_space", " echo spaced\necho kept")

		// The leading space is preserved: only HIST_REDUCE_BLANKS trims.
		z.assertRecorded(" echo spaced", "echo kept")
	})
}

// HIST_REDUCE_BLANKS: runs of whitespace collapse before the command is
// recorded.
func TestZshReduceBlanks(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		z := newZshShell(t)
		z.run("setopt hist_reduce_blanks", "echo    wide     gaps")

		z.assertRecorded("echo wide gaps")
	})

	t.Run("off", func(t *testing.T) {
		z := newZshShell(t)
		z.run("unsetopt hist_reduce_blanks", "echo    wide     gaps")

		z.assertRecorded("echo    wide     gaps")
	})
}

// HIST_IGNORE_DUPS: a command identical to the one before it is not recorded.
func TestZshIgnoreDups(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		z := newZshShell(t)
		z.run("setopt hist_ignore_dups", "echo twice\necho twice\necho after")

		z.assertRecorded("echo twice", "echo after")
	})

	t.Run("off", func(t *testing.T) {
		z := newZshShell(t)
		z.run("unsetopt hist_ignore_dups", "echo twice\necho twice")

		if got := z.recorded(); len(got) != 2 {
			t.Errorf("recorded %q, want the command twice", got)
		}
	})
}

// HIST_NO_FUNCTIONS: function definitions are not recorded.
func TestZshNoFunctions(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		z := newZshShell(t)
		z.run("setopt hist_no_functions",
			"myfunc() { echo hi; }\nfunction otherfunc { echo hi; }\necho kept")

		z.assertRecorded("echo kept")
	})

	t.Run("off", func(t *testing.T) {
		z := newZshShell(t)
		z.run("unsetopt hist_no_functions", "myfunc() { echo hi; }")

		z.assertRecorded("myfunc() { echo hi; }")
	})
}

// HIST_NO_STORE: the commands used to read history back are not recorded.
func TestZshNoStore(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		z := newZshShell(t)
		z.run("setopt hist_no_store", "history\nfc -l\necho kept")

		z.assertRecorded("echo kept")
	})

	t.Run("off", func(t *testing.T) {
		z := newZshShell(t)
		z.run("unsetopt hist_no_store", "history")

		z.assertRecorded("history")
	})
}

// SHARE_HISTORY: off means a search only reports this shell's own commands.
func TestZshShareHistory(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		z := newZshShell(t)
		z.seed("another-shell", "from another shell", 100)

		out := z.run("unsetopt share_history", "echo mine\n"+settleCmd+"histdb -n 20")

		if !strings.Contains(out, "echo mine") {
			t.Errorf("output missing this session's command:\n%s", out)
		}
		if strings.Contains(out, "from another shell") {
			t.Errorf("leaked another session:\n%s", out)
		}
	})

	t.Run("on", func(t *testing.T) {
		z := newZshShell(t)
		z.seed("another-shell", "from another shell", 100)

		out := z.run("setopt share_history", "echo mine\n"+settleCmd+"histdb -n 20")

		if !strings.Contains(out, "from another shell") {
			t.Errorf("did not include another session:\n%s", out)
		}
	})
}

// HIST_FIND_NO_DUPS: a search reports each command once, its newest run.
func TestZshFindNoDups(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		z := newZshShell(t)
		out := z.run("setopt hist_find_no_dups\nunsetopt hist_ignore_dups",
			"echo dup\necho dup\necho dup\n"+settleCmd+"histdb -n 20 --like 'echo dup'")

		if got := strings.Count(out, "echo dup\n"); got != 1 {
			t.Errorf("listed %d times, want 1:\n%s", got, out)
		}
	})

	t.Run("off", func(t *testing.T) {
		z := newZshShell(t)
		out := z.run("unsetopt hist_find_no_dups\nunsetopt hist_ignore_dups",
			"echo dup\necho dup\necho dup\n"+settleCmd+"histdb -n 20 --like 'echo dup'")

		if got := strings.Count(out, "echo dup\n"); got < 3 {
			t.Errorf("listed %d times, want every run:\n%s", got, out)
		}
	})
}

// The wrapper adds flags of its own, and they have to land after any
// subcommand or the binary reads the subcommand as a search pattern.
func TestZshWrapperKeepsSubcommands(t *testing.T) {
	z := newZshShell(t)

	// NO_SHARE_HISTORY is what makes the wrapper add a flag at all.
	out := z.run("unsetopt share_history",
		"git status\ngit status\n"+settleCmd+"histdb search -F --columns cmd --like 'git%'")

	// One column, so the last line is the command and nothing else.
	if got, want := strings.TrimSpace(out), "git status"; !strings.HasSuffix(got, want) {
		t.Errorf("output = %q, want it to end with %q", got, want)
	}
}

// HIST_IGNORE_ALL_DUPS and HIST_SAVE_NO_DUPS are documented as unsupported,
// since histdb keeps every run of a command.
func TestZshDupPruningOptionsAreIgnored(t *testing.T) {
	for _, opt := range []string{"hist_ignore_all_dups", "hist_save_no_dups"} {
		t.Run(opt, func(t *testing.T) {
			z := newZshShell(t)
			z.run("setopt "+opt+"\nunsetopt hist_ignore_dups", "echo a\necho b\necho a")

			if got := z.recorded(); len(got) != 3 {
				t.Errorf("recorded %q, want all three runs kept", got)
			}
		})
	}
}

// INC_APPEND_HISTORY_TIME asks zsh to defer writing until a command finishes.
// histdb always writes at both ends, so the row lands either way.
func TestZshIncAppendHistoryTimeIsIgnored(t *testing.T) {
	z := newZshShell(t)
	z.run("setopt inc_append_history_time", "echo kept")

	z.assertRecorded("echo kept")
}

// -S is how you see other sessions when NO_SHARE_HISTORY has the wrapper
// passing --session for you.
func TestZshAllSessionsOverridesShareHistory(t *testing.T) {
	z := newZshShell(t)
	z.seed("another-shell", "from another shell", 100)

	out := z.run("unsetopt share_history",
		"echo mine\n"+settleCmd+"histdb -S --columns cmd")

	if !strings.Contains(out, "from another shell") {
		t.Errorf("-S missing the other session's command:\n%s", out)
	}
}

// import is a subcommand, so the wrapper must hand it over whole rather than
// treat it as a search with flags of its own.
func TestZshWrapperPassesImportThrough(t *testing.T) {
	z := newZshShell(t)

	histfile := filepath.Join(t.TempDir(), "zsh_history")
	if err := os.WriteFile(histfile, []byte(": 1700000000:0;from a histfile\n"), 0o600); err != nil {
		t.Fatalf("write histfile: %v", err)
	}

	out := z.run("unsetopt share_history",
		"histdb import zsh "+histfile+"\n"+settleCmd+"histdb -S --columns cmd")

	if !strings.Contains(out, "imported 1 command") {
		t.Errorf("import did not run through the wrapper:\n%s", out)
	}
	if !strings.Contains(out, "from a histfile") {
		t.Errorf("imported command missing from the listing:\n%s", out)
	}
}

// The database has to reach anything the shell starts, and a value set before
// sourcing is the caller's choice, not something to overwrite.
func TestZshExportsDatabasePath(t *testing.T) {
	z := newZshShell(t)

	out := z.run("", "printenv HISTDB_FILE")

	if !strings.Contains(out, z.db) {
		t.Errorf("HISTDB_FILE not exported as %q:\n%s", z.db, out)
	}
}
