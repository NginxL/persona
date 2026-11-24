package resourcequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SSH uses argument vectors and a fixed remote vocabulary. It never invokes a local shell.
func SSH(ctx context.Context, m Mirror, words []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if m.Target == "" || strings.HasPrefix(m.Target, "-") || strings.ContainsAny(m.Target, " \t\n\r") {
		return errors.New("invalid runner SSH target")
	}
	for _, s := range words {
		if !ValidRef(s) && s != "--json" && s != "--tag" {
			return errors.New("invalid runner argument")
		}
	}
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "IdentitiesOnly=yes"}
	if m.Port != 0 {
		args = append(args, "-p", strconv.Itoa(m.Port))
	}
	if m.Identity != "" {
		args = append(args, "-i", m.Identity)
	}
	args = append(args, m.Target, strings.Join(words, " "))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

type boundedBuffer struct{ data []byte }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data)+n > 1<<20 {
		return 0, errors.New("runner response exceeds 1 MiB")
	}
	b.data = append(b.data, p...)
	return n, nil
}
func ReadQueue(ctx context.Context, m Mirror) (Snapshot, error) {
	var out, errout boundedBuffer
	err := SSH(ctx, m, []string{"queue", "--json"}, nil, &out, &errout)
	now := time.Now().UTC()
	snap := Snapshot{AttemptedAt: now, Unknown: true}
	if err == nil {
		if q, e := ParseQueue(out.data); e == nil {
			snap.Queue = q
			snap.ObservedAt = now
			snap.Unknown = false
			return snap, nil
		}
		if json.Valid(out.data) {
			snap.Raw = string(out.data)
			return snap, nil
		}
	}
	// Old runners may reject --json. Plain text is display-only; never derive a successful result from it.
	out = boundedBuffer{}
	errout = boundedBuffer{}
	if e := SSH(ctx, m, []string{"queue"}, nil, &out, &errout); e == nil {
		snap.Raw = string(out.data)
		return snap, nil
	}
	return snap, fmt.Errorf("runner queue unavailable")
}
func ReadResult(ctx context.Context, m Mirror, project, commit string, groups []string) (*Outcome, error) {
	var err error
	groups, err = NormalizeGroups(groups)
	if err != nil {
		return nil, err
	}
	if _, err := Key(project, commit, groups); err != nil {
		return nil, err
	}
	words := append([]string{"result", project, commit}, groups...)
	words = append(words, "--json")
	var out, errout boundedBuffer
	if err := SSH(ctx, m, words, nil, &out, &errout); err != nil {
		return nil, errors.New("runner result unavailable")
	}
	return ParseResult(out.data)
}
