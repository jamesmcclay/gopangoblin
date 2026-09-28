// Package tool holds the registry gopangoblin's tools register their
// cobra.Command into, so main.go can add them all to the root command
// without importing each tool package by name.
package tool

import "github.com/spf13/cobra"

var commands []*cobra.Command

// Register adds cmd to the set main.go attaches to the root command. Call
// from an init() in the tool's own package.
func Register(cmd *cobra.Command) {
	commands = append(commands, cmd)
}

// All returns every registered command, in registration order.
func All() []*cobra.Command {
	return commands
}
