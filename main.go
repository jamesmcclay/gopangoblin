// Command gopangoblin runs tools related to Palo Alto Networks technologies.
// Built as "pang" by convention (see README.md / setup.ps1), but usage and
// error output always reflect whatever the binary is actually named.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	_ "github.com/jamesmcclay/gopangoblin/internal/habuilder"
	_ "github.com/jamesmcclay/gopangoblin/internal/internet"
	_ "github.com/jamesmcclay/gopangoblin/internal/reset"
	_ "github.com/jamesmcclay/gopangoblin/internal/sdwan"
	"github.com/jamesmcclay/gopangoblin/internal/tool"
	_ "github.com/jamesmcclay/gopangoblin/internal/update"
)

func main() {
	root := &cobra.Command{
		Use:           filepath.Base(os.Args[0]),
		Short:         "Tools for Palo Alto Networks Strata Cloud Manager automation",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	for _, cmd := range tool.All() {
		root.AddCommand(cmd)
	}

	executed, err := root.ExecuteC()
	if err != nil {
		if executed != nil && executed != root {
			fmt.Fprintf(os.Stderr, "%s %s: %v\n", root.Name(), executed.Name(), err)
		} else {
			fmt.Fprintf(os.Stderr, "%s: %v\n", root.Name(), err)
		}
		os.Exit(1)
	}
}
