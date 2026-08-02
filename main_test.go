package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersionFlags(t *testing.T) {
	for _, arg := range []string{"-v", "--version"} {
		var stdout, stderr bytes.Buffer

		if err := run([]string{arg}, &stdout, &stderr); err != nil {
			t.Fatalf("run %s: %v", arg, err)
		}
		if got, want := stdout.String(), "histdb 0.0.1\n"; got != want {
			t.Errorf("%s stdout = %q, want %q", arg, got, want)
		}
	}
}

func TestRunHelpFlags(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		var stdout, stderr bytes.Buffer

		if err := run([]string{arg}, &stdout, &stderr); err != nil {
			t.Fatalf("run %s: %v", arg, err)
		}
		if !strings.Contains(stdout.String(), "init <shell>") {
			t.Errorf("%s help missing init usage: %q", arg, stdout.String())
		}
	}
}

func TestRunNoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := run(nil, &stdout, &stderr); err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr missing usage: %q", stderr.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run([]string{"bogus"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), `unknown command "bogus"`) {
		t.Errorf("err = %v", err)
	}
}

// getopt(3) syntax: -v is short, --version is long, -version is neither.
func TestRunRejectsSingleDashLongFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := run([]string{"-version"}, &stdout, &stderr); err == nil {
		t.Fatal("want error, got nil")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunCombinedShortFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := run([]string{"-hv"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "usage:") {
		t.Errorf("stdout = %q, want help", stdout.String())
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := run([]string{"-x"}, &stdout, &stderr); err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr missing usage: %q", stderr.String())
	}
}

func TestInitZsh(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := run([]string{"init", "zsh"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "# histdb 0.0.1 init for zsh\n") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "_histdb_hello") {
		t.Errorf("missing snippet body: %q", out)
	}
}

func TestInitUnsupportedShell(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run([]string{"init", "tcsh"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), `unsupported shell "tcsh"`) {
		t.Errorf("err = %v", err)
	}
}

func TestInitRequiresShellArg(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := run([]string{"init"}, &stdout, &stderr); err == nil {
		t.Fatal("want error, got nil")
	}
}
