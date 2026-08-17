package main

// Tests for the zsh history options histdb honors, driven through a real zsh
// so the hooks and wrapper in shell/zsh.zsh are what run, not a Go stand-in.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A version manager or a script can rewrite PATH after the hooks install.
func TestZshRecordsAfterPathLosesHistdb(t *testing.T) {
	z := newZshShell(t)
	z.run("", "PATH=/nonexistent\necho offpath")

	z.assertRecorded("PATH=/nonexistent", "echo offpath")
}

// HIST_IGNORE_SPACE: a command whose first character is a space is not
// recorded.
func TestZshIgnoreSpace(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

// HIST_IGNORE_DUPS keeps a repeat out of zsh's own history, not out of histdb:
// if you ran it, it is recorded, and duplicates are a matter for searching and
// purging.
func TestZshIgnoreDupsRecordsBothRuns(t *testing.T) {
	z := newZshShell(t)
	z.run("setopt hist_ignore_dups", "echo twice\necho twice\necho after")

	z.assertRecorded("echo twice", "echo twice", "echo after")
}

// HIST_IGNORE_ALL_DUPS, same again.
func TestZshIgnoreAllDupsRecordsEveryRun(t *testing.T) {
	z := newZshShell(t)
	z.run("setopt hist_ignore_all_dups", "echo A\necho B\necho A")

	z.assertRecorded("echo A", "echo B", "echo A")
}

// HIST_NO_FUNCTIONS keeps a function definition out of zsh's history. histdb
// still records it: enabling histdb is itself the choice to keep history in
// SQLite.
func TestZshNoFunctionsRecordsAnyway(t *testing.T) {
	z := newZshShell(t)
	z.run("setopt hist_no_functions",
		"myfunc() { echo hi; }\nfunction otherfunc { echo hi; }\necho kept")

	z.assertRecorded("myfunc() { echo hi; }", "function otherfunc { echo hi; }",
		"echo kept")
}

// HIST_NO_STORE, same again.
func TestZshNoStoreRecordsAnyway(t *testing.T) {
	z := newZshShell(t)
	z.run("setopt hist_no_store", "history\nfc -l\necho kept")

	z.assertRecorded("history", "fc -l", "echo kept")
}

// SHARE_HISTORY: off means a search only reports this shell's own commands.
func TestZshShareHistory(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
