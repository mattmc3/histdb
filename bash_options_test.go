package main

// Tests for the bash side of the integration, driven through a real bash so
// the DEBUG trap and PROMPT_COMMAND hooks in shell/bash.bash are what run.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// $BASH_COMMAND is one simple command, so a pipeline would come back as its
// first element. The hooks read the typed line out of the history list.
func TestBashRecordsWholePipeline(t *testing.T) {
	b := newBashShell(t)
	out := b.run("", "true | false\n"+settleCmd+"histdb --columns cmd,ret --like 'true%'")

	b.assertRecorded("true | false", "sleep 1", "histdb --columns cmd,ret --like 'true%'")
	if !strings.Contains(out, "true | false  1") {
		t.Errorf("want the pipeline's last status:\n%s", out)
	}
}

// Every status in the pipeline, not only the last. Reading $? first, or
// letting a `local` of its own run before the copy, leaves one zero behind.
func TestBashRecordsPipeStatus(t *testing.T) {
	b := newBashShell(t)
	b.run("", "true | false | true")

	if got, want := b.pipeStatus("true | false | true"), "0,1,0"; got != want {
		t.Errorf("pipestatus = %q, want %q", got, want)
	}
}

// Running the DEBUG trap takes $_ with it, so the trap hands it back. Without
// that, `mkdir foo && cd $_` walks into the name of the hook.
func TestBashKeepsLastArgument(t *testing.T) {
	b := newBashShell(t)
	out := b.run("", "echo one two\necho \"under=[$_]\"")

	if !strings.Contains(out, "under=[two]") {
		t.Errorf("$_ not preserved across the trap:\n%s", out)
	}
}

// A version manager or a script can rewrite PATH after the hooks install.
func TestBashRecordsAfterPathLosesHistdb(t *testing.T) {
	b := newBashShell(t)
	b.run("", "PATH=/nonexistent\necho offpath")

	b.assertRecorded("PATH=/nonexistent", "echo offpath")
}

// A command is keyed by its start, so commands close enough together to share
// a clock reading still have to land as separate rows.
func TestBashKeepsCommandsFromTheSameSecond(t *testing.T) {
	b := newBashShell(t)
	b.run("", "echo one\necho two\necho three")

	b.assertRecorded("echo one", "echo two", "echo three")
}

// HISTCONTROL=ignorespace: a command whose first character is a space is not
// recorded. Leading space is the one way to say do not record this at all, so
// it is the one setting left with bash.
func TestBashIgnoreSpace(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		b := newBashShell(t)
		b.run("HISTCONTROL=ignorespace", " echo secret\necho kept")

		b.assertRecorded("echo kept")
	})

	t.Run("off", func(t *testing.T) {
		b := newBashShell(t)
		b.run("HISTCONTROL=", " echo spaced\necho kept")

		b.assertRecorded(" echo spaced", "echo kept")
	})
}

// HISTCONTROL=ignoredups keeps a repeat out of bash's own history file, not
// out of histdb: if you ran it, it is recorded, and duplicates are a matter
// for searching and purging.
func TestBashIgnoreDupsRecordsBothRuns(t *testing.T) {
	b := newBashShell(t)
	b.run("HISTCONTROL=ignoredups", "echo twice\necho twice\necho after")

	b.assertRecorded("echo twice", "echo twice", "echo after")
	if got := count(b.historyFile(), "echo twice"); got != 1 {
		t.Errorf("history file has %d copies of the command, want 1", got)
	}
}

// HISTCONTROL=erasedups, same again. The history file is served as though
// ignoredups were set, since erasing an older copy means scanning the list.
func TestBashEraseDupsRecordsEveryRun(t *testing.T) {
	b := newBashShell(t)
	b.run("HISTCONTROL=erasedups", "echo A\necho B\necho A\necho A")

	b.assertRecorded("echo A", "echo B", "echo A", "echo A")
	if got := count(b.historyFile(), "echo A"); got != 2 {
		t.Errorf("history file has %d copies of the command, want 2", got)
	}
}

// HISTIGNORE keeps a command out of bash's history file. histdb still records
// it: enabling histdb is itself the choice to keep history in SQLite.
func TestBashHistIgnoreRecordsAnyway(t *testing.T) {
	b := newBashShell(t)
	b.run("HISTIGNORE='pwd:echo dropped*'", "pwd\necho dropped this\necho kept")

	b.assertRecorded("pwd", "echo dropped this", "echo kept")

	file := b.historyFile()
	for _, gone := range []string{"pwd", "echo dropped this"} {
		if count(file, gone) != 0 {
			t.Errorf("history file kept %q, which HISTIGNORE excludes", gone)
		}
	}
	if count(file, "echo kept") != 1 {
		t.Errorf("history file lost %q:\n%q", "echo kept", file)
	}
}

// A HISTIGNORE pattern of `&` means the line before it.
func TestBashHistIgnoreAmpersand(t *testing.T) {
	b := newBashShell(t)
	b.run("HISTIGNORE='&'", "echo D\necho D\necho E")

	b.assertRecorded("echo D", "echo D", "echo E")
	if got := count(b.historyFile(), "echo D"); got != 1 {
		t.Errorf("history file has %d copies of the command, want 1", got)
	}
}

// The settings are read again every prompt, so one changed after the hooks
// installed is honored like one set before them.
func TestBashHistIgnoreSetAfterLoading(t *testing.T) {
	b := newBashShell(t)
	b.run("", "HISTIGNORE='echo late*'\necho late one\necho kept")

	b.assertRecorded("HISTIGNORE='echo late*'", "echo late one", "echo kept")
	if count(b.historyFile(), "echo late one") != 0 {
		t.Error("history file kept a command HISTIGNORE excludes")
	}
}

// `set +o history` freezes the list, so the whole line cannot be read back.
// The command is still recorded, from the one simple command bash names.
func TestBashRecordsWithHistoryOff(t *testing.T) {
	b := newBashShell(t)
	b.run("", "set +o history\necho nohistory\nset -o history\necho back")

	b.assertRecorded("set +o history", "echo nohistory", "set -o history", "echo back")
}

// HISTSIZE=0 leaves no list to read either.
func TestBashRecordsWithoutHistoryList(t *testing.T) {
	b := newBashShell(t)
	b.run("HISTSIZE=0", "echo zero\necho zero")

	b.assertRecorded("echo zero", "echo zero")
}

// The hooks go in front of whatever the prompt already did, and leave it
// running.
func TestBashKeepsExistingPromptCommand(t *testing.T) {
	b := newBashShell(t)
	out := b.run("PROMPT_COMMAND='echo theirs'", "echo mine")

	if !strings.Contains(out, "theirs") {
		t.Errorf("the existing PROMPT_COMMAND stopped running:\n%s", out)
	}
	b.assertRecorded("echo mine")
}

// A DEBUG trap already installed belongs to somebody, and replacing it would
// take their hook away without saying so.
func TestBashLeavesAnExistingDebugTrap(t *testing.T) {
	b := newBashShell(t)
	out := b.run("trap 'echo theirtrap' DEBUG", "echo mine")

	if !strings.Contains(out, "theirtrap") {
		t.Errorf("the existing DEBUG trap stopped running:\n%s", out)
	}
	if !strings.Contains(b.stderr, "DEBUG trap is already installed") {
		t.Errorf("nothing said the hooks did not install:\n%s", b.stderr)
	}
}

// Where bash-preexec is already loaded it owns the DEBUG trap, and histdb
// hooks onto it rather than take the trap away.
func TestBashHooksOntoBashPreexec(t *testing.T) {
	b := newBashShell(t)

	stub := filepath.Join(b.dir, "bash-preexec.sh")
	if err := os.WriteFile(stub, []byte(bashPreexecStub), 0o600); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	b.run("source "+stub, "echo hooked\necho hooked")

	b.assertRecorded("echo hooked", "echo hooked")
	if got := b.pipeStatus("echo hooked"); got != "0" {
		t.Errorf("pipestatus = %q, want %q", got, "0")
	}
}

// Enough of bash-preexec to exercise the branch that hooks onto it: it owns
// the DEBUG trap, hands preexec the whole line, and calls precmd with the
// exit status restored.
const bashPreexecStub = `
__bp_imported=1
preexec_functions=()
precmd_functions=()
__bp_at_prompt=1
__bp_ret=0
__bp_set_ret() { return ${1:-0}; }
__bp_debug() {
  [[ -n $__bp_at_prompt ]] || return 0
  __bp_at_prompt=
  local line f
  line=$(HISTTIMEFORMAT= builtin history 1)
  [[ $line =~ ^[[:space:]]*[0-9]+[[:space:]]+(.*)$ ]] && line=${BASH_REMATCH[1]}
  for f in "${preexec_functions[@]}"; do "$f" "$line"; done
}
__bp_precmd() {
  __bp_ret=$?
  local f
  for f in "${precmd_functions[@]}"; do __bp_set_ret "$__bp_ret"; "$f"; done
  __bp_at_prompt=1
}
trap '__bp_debug' DEBUG
PROMPT_COMMAND=__bp_precmd
`

// Sourcing the integration a second time must not hook a second time, or every
// command lands twice.
func TestBashSourcingTwiceRecordsOnce(t *testing.T) {
	b := newBashShell(t)
	b.run("", shells["bash"].load+"\necho once")

	b.assertRecorded(shells["bash"].load, "echo once")
}

// Bash has no SHARE_HISTORY, so a search reaches every session without being
// asked to.
func TestBashSearchesEverySession(t *testing.T) {
	b := newBashShell(t)
	b.seed("another-shell", "from another shell", 100)

	out := b.run("", "echo mine\n"+settleCmd+"histdb --columns cmd")

	for _, want := range []string{"echo mine", "from another shell"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// The bash `histdb` function calls the pinned binary, so every subcommand has
// to reach it whole.
func TestBashWrapperPassesImportThrough(t *testing.T) {
	b := newBashShell(t)

	histfile := filepath.Join(t.TempDir(), "bash_history")
	if err := os.WriteFile(histfile, []byte("#1700000000\nfrom a histfile\n"), 0o600); err != nil {
		t.Fatalf("write histfile: %v", err)
	}

	out := b.run("", "histdb import bash "+histfile+"\n"+settleCmd+"histdb --columns cmd")

	if !strings.Contains(out, "imported 1 command") {
		t.Errorf("import did not run through the wrapper:\n%s\nSTDERR:\n%s", out, b.stderr)
	}
	if !strings.Contains(out, "from a histfile") {
		t.Errorf("imported command missing from the listing:\n%s", out)
	}
}

// The database has to reach anything the shell starts, and a value set before
// sourcing is the caller's choice, not something to overwrite.
func TestBashExportsDatabasePath(t *testing.T) {
	b := newBashShell(t)

	out := b.run("", "printenv HISTDB_FILE")

	if !strings.Contains(out, b.db) {
		t.Errorf("HISTDB_FILE not exported as %q:\n%s", b.db, out)
	}
}
