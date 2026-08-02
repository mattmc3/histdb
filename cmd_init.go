package main

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

//go:embed shell/*
var shellFS embed.FS

// snippets maps a shell name to its integration file. Adding a shell means a
// file in shell/ and an entry here.
var snippets = map[string]string{
	"zsh": "shell/zsh.zsh",
}

func runInit(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: histdb init <%s>", strings.Join(supportedShells(), "|"))
	}

	file, ok := snippets[args[0]]
	if !ok {
		return fmt.Errorf("unsupported shell %q, want one of: %s",
			args[0], strings.Join(supportedShells(), ", "))
	}
	snippet, err := shellFS.ReadFile(file)
	if err != nil {
		return errors.New("read snippet: " + err.Error())
	}

	fmt.Fprintf(stdout, "# histdb %s init for %s\n", version, args[0])
	_, err = stdout.Write(snippet)
	return err
}

func supportedShells() []string {
	return slices.Sorted(maps.Keys(snippets))
}
