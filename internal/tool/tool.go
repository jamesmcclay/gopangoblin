// Package tool defines the registry that main.go dispatches into.
// Each gopangoblin tool (habuilder, and any future tool) registers itself
// here so the CLI can list and run tools by name.
package tool

import "fmt"

// Tool is a single gopangoblin subcommand, e.g. "habuilder".
type Tool interface {
	Name() string
	Summary() string
	Run(args []string) error
}

var registry = map[string]Tool{}
var order []string

// Register adds a tool to the registry. Call from an init() in the tool's package.
func Register(t Tool) {
	if _, exists := registry[t.Name()]; exists {
		panic(fmt.Sprintf("tool %q already registered", t.Name()))
	}
	registry[t.Name()] = t
	order = append(order, t.Name())
}

// Get looks up a registered tool by name.
func Get(name string) (Tool, bool) {
	t, ok := registry[name]
	return t, ok
}

// All returns registered tools in registration order.
func All() []Tool {
	tools := make([]Tool, 0, len(order))
	for _, name := range order {
		tools = append(tools, registry[name])
	}
	return tools
}
