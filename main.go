// Command gopangoblin runs tools related to Palo Alto Networks technologies.
// Built as "pang" by convention (see README.md / setup.ps1), but usage and
// error output always reflect whatever the binary is actually named.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/jamesmcclay/gopangoblin/internal/habuilder"
	_ "github.com/jamesmcclay/gopangoblin/internal/internet"
	_ "github.com/jamesmcclay/gopangoblin/internal/reset"
	"github.com/jamesmcclay/gopangoblin/internal/tool"
	_ "github.com/jamesmcclay/gopangoblin/internal/update"
)

var progName = filepath.Base(os.Args[0])

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	name := os.Args[1]
	if name == "-h" || name == "--help" || name == "help" {
		usage()
		return
	}

	t, ok := tool.Get(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: unknown tool %q\n\n", progName, name)
		usage()
		os.Exit(1)
	}

	if err := t.Run(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s %s: %v\n", progName, name, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: %s <tool> [flags]\n", progName)
	fmt.Fprintln(os.Stderr, "\navailable tools:")
	for _, t := range tool.All() {
		fmt.Fprintf(os.Stderr, "  %-12s %s\n", t.Name(), t.Summary())
	}
}
