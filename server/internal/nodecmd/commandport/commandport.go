// Package commandport is where a node-side module hands the device's command line (internal/nodecmd) the commands it
// adds to `personad` / `persona-node`, such as `personad office …` (docs/architecture/core-and-modules.md "The node's
// modules").
//
// The core declares here what it needs, in its own terms and never as a module's types; the node's composition root
// (internal/modules/all/node, linked by internal/nodecmd) provides it in the init of the module's file, under the
// module's build constraint. With the module left out nothing is provided: the command is unknown, exactly as before
// the module existed.
//
// Everything is provided while the program initializes, before main runs, and only read afterwards.
package commandport

import (
	"context"
	"io"
	"slices"
	"strings"
)

// Command is a device command a module adds: it runs with the arguments after its name and returns the exit code.
type Command func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int

type command struct {
	run   Command
	usage string
}

var commands = map[string]command{}

// AddCommand provides the device command name and its lines in the usage text, written as the other device commands'
// lines are ("  personad <name> …" and its indented explanation, without a trailing newline). Init only.
func AddCommand(name, usage string, run Command) {
	if _, dup := commands[name]; dup {
		panic("commandport: command " + name + " provided twice")
	}
	commands[name] = command{run, usage}
}

// LookupCommand is the command name runs, if a module in this build provides one.
func LookupCommand(name string) (Command, bool) {
	c, ok := commands[name]
	return c.run, ok
}

// CommandUsage is the usage lines of the provided commands, by name, each ending in a newline ("" without any).
func CommandUsage() string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(strings.TrimRight(commands[name].usage, "\n"))
		b.WriteByte('\n')
	}
	return b.String()
}
