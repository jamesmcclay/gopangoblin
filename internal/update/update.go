// Package update implements the "update" gopangoblin tool: it pulls a
// fresh copy of the gopangoblin source from GitHub and rebuilds the binary,
// mirroring the download-a-zip approach setup.ps1 uses rather than assuming
// a git checkout is present.
package update

import (
	"flag"
	"fmt"
	"runtime"

	"github.com/jamesmcclay/gopangoblin/internal/tool"
)

func init() {
	tool.Register(&Tool{})
}

const defaultRepo = "https://github.com/jamesmcclay/gopangoblin"

// Tool is the "update" gopangoblin tool.
type Tool struct{}

func (t *Tool) Name() string { return "update" }

func (t *Tool) Summary() string {
	return "Pull the latest gopangoblin source from GitHub and rebuild"
}

func (t *Tool) Run(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	repo := fs.String("repo", defaultRepo, "GitHub repo URL to update from")
	branch := fs.String("branch", "main", "branch to pull")
	output := fs.String("output", defaultOutputName(), "path to write the rebuilt binary to")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := findRepoRoot(".")
	if err != nil {
		return err
	}
	fmt.Printf("update: repo root %s\n", root)

	zipURL := fmt.Sprintf("%s/archive/refs/heads/%s.zip", *repo, *branch)
	fmt.Printf("update: downloading %s\n", zipURL)
	zipPath, err := downloadToTemp(zipURL)
	if err != nil {
		return fmt.Errorf("downloading source: %w", err)
	}
	defer removeQuietly(zipPath)

	fmt.Println("update: extracting")
	srcDir, cleanup, err := extractZip(zipPath)
	if err != nil {
		return fmt.Errorf("extracting source: %w", err)
	}
	defer cleanup()

	fmt.Println("update: syncing source files")
	if err := syncSource(srcDir, root); err != nil {
		return fmt.Errorf("syncing source: %w", err)
	}

	fmt.Printf("update: building %s\n", *output)
	if err := build(root, *output); err != nil {
		return fmt.Errorf("building: %w", err)
	}

	fmt.Printf("update: rebuilt %s from %s@%s\n", *output, *repo, *branch)
	return nil
}

func defaultOutputName() string {
	if runtime.GOOS == "windows" {
		return "pang.exe"
	}
	return "pang"
}
