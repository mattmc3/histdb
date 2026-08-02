package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mattmc3/getopt"
)

var version = "0.0.1"

//go:embed shell/*.sh
var shellFS embed.FS

const usage = `histdb - shell history in SQLite

usage:
  histdb [options] <command> [args]

commands:
  init <shell>   print shell integration to eval

options:
  -h, --help     print this help
  -v, --version  print version

supported shells: zsh

enable in zsh:
  source <(histdb init zsh)
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "histdb: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	// getopt prints its own errors and usage, so silence it and own both here.
	fs := getopt.NewFlagSet("histdb", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	showHelp := fs.Define("help", false, "print this help")
	showVersion := fs.Define("version", false, "print version")
	fs.Aliases("h", "help", "v", "version")

	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, usage)
		return err
	}

	switch {
	case *showHelp:
		fmt.Fprint(stdout, usage)
		return nil
	case *showVersion:
		fmt.Fprintf(stdout, "histdb %s\n", version)
		return nil
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("no command given")
	}

	switch rest[0] {
	case "init":
		return initShell(rest[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q, try 'histdb --help'", rest[0])
	}
}

func initShell(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: histdb init <shell>")
	}

	// Shell snippets live in shell/ and are embedded at build time.
	snippet, err := shellFS.ReadFile("shell/" + args[0] + ".sh")
	if err != nil {
		return fmt.Errorf("unsupported shell %q", args[0])
	}

	fmt.Fprintf(stdout, "# histdb %s init for %s\n", version, args[0])
	_, err = stdout.Write(snippet)
	return err
}
