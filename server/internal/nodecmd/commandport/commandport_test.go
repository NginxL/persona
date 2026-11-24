package commandport

import (
	"context"
	"io"
	"testing"
)

func TestCommands(t *testing.T) {
	saved := commands
	t.Cleanup(func() { commands = saved })
	commands = map[string]command{}
	if CommandUsage() != "" {
		t.Fatal("usage without commands")
	}
	if _, ok := LookupCommand("x"); ok {
		t.Fatal("x without a module")
	}
	AddCommand("y", "  personad y   the y command\n", func(context.Context, []string, io.Reader, io.Writer, io.Writer) int { return 2 })
	AddCommand("x", "  personad x   the x command", func(context.Context, []string, io.Reader, io.Writer, io.Writer) int { return 1 })
	run, ok := LookupCommand("x")
	if !ok || run(context.Background(), nil, nil, io.Discard, io.Discard) != 1 {
		t.Fatal("x is not the provided command")
	}
	if got, want := CommandUsage(), "  personad x   the x command\n  personad y   the y command\n"; got != want {
		t.Fatalf("usage %q, want %q", got, want)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a command provided twice does not panic")
		}
	}()
	AddCommand("x", "", nil)
}
