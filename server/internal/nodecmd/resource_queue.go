package nodecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
	rq "github.com/nwverse/persona/server/internal/resourcequeue"
)

var resourceCall = func(ctx context.Context, name string, args any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := nodeclient.DialInterruptible(ctx, map[string]any{"kind": "cli.call", "name": name, "args": args, "pane": nodeclient.HookPane(os.Getenv), "codex_thread_id": os.Getenv("CODEX_THREAD_ID")})
	if err != nil {
		return err
	}
	var res proto.ToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	for _, c := range res.Content {
		if c.Text != "" {
			if res.IsError {
				return errors.New(c.Text)
			}
			return json.Unmarshal([]byte(c.Text), out)
		}
	}
	return errors.New("empty resource response")
}

func resourceCommand(ctx context.Context, args []string) error {
	if len(args) < 1 || args[0] != "status" || len(args) > 2 {
		return UsageError("usage: personad resource status [resource]")
	}
	id := ""
	if len(args) == 2 {
		id = args[1]
	}
	var out json.RawMessage
	if err := resourceCall(ctx, "resource_status", rq.Request{Resource: id}, &out); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
func fallbackMirror() rq.Mirror {
	port, _ := strconv.Atoi(os.Getenv("PERSONA_GATE_SSH_PORT"))
	return rq.Mirror{Target: os.Getenv("PERSONA_GATE_SSH_TARGET"), Port: port, Identity: os.Getenv("PERSONA_GATE_SSH_IDENTITY")}
}
func gateCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Bound the whole invocation as well as each RPC when the manager stays unreachable.
	ctx, stop := context.WithTimeout(ctx, 3*time.Hour)
	defer stop()
	if len(args) == 1 {
		args = strings.Fields(args[0])
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: personad gate <project> <ref> [groups...] (or quote the complete request)")
		return 2
	}
	project, ref := args[0], args[1]
	groups, err := rq.NormalizeGroups(args[2:])
	if err != nil || !rq.ValidRef(project) || !rq.ValidRef(ref) {
		fmt.Fprintln(stderr, "invalid gate request")
		return 2
	}
	fmt.Fprintln(stderr, "RESOURCE_STATUS=checking")
	var state struct {
		Resources []rq.Status `json:"resources"`
	}
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = resourceCall(check, "resource_status", map[string]any{}, &state)
	cancel()
	var def rq.Definition
	var snap rq.Snapshot
	if err != nil {
		mirror := fallbackMirror()
		if mirror.Target == "" {
			fmt.Fprintln(stderr, "resource manager unavailable; set the fallback runner SSH target to continue")
			return 69
		}
		fmt.Fprintln(stderr, "DEGRADED: resource manager unavailable; using the runner's existing queue without coalescing")
		return gateSSH(ctx, mirror, append([]string{project, ref}, groups...), stdin, stdout, stderr)
	}
	if len(state.Resources) != 1 {
		fmt.Fprintln(stderr, "gate requires exactly one registered runner")
		return 2
	}
	def = state.Resources[0].Definition
	snap = state.Resources[0].MirrorState
	commit := ref
	var input io.Reader
	if ref == "bundle" {
		f, e := os.CreateTemp("", "persona-gate-*.bundle")
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 69
		}
		defer os.Remove(f.Name())
		defer f.Close()
		n, e := io.Copy(f, io.LimitReader(stdin, (128<<20)+1))
		if e != nil || n > 128<<20 {
			fmt.Fprintln(stderr, "bundle exceeds 128 MiB or cannot be read")
			return 2
		}
		raw, e := exec.CommandContext(ctx, "git", "bundle", "list-heads", f.Name()).Output()
		heads := strings.Fields(string(raw))
		if e != nil || len(heads) != 2 {
			fmt.Fprintln(stderr, "bundle must contain exactly one reference")
			return 2
		}
		commit = heads[0]
		_, _ = f.Seek(0, io.SeekStart)
		input = f
	} else if _, e := rq.Key(project, commit, groups); e != nil {
		repo := def.Repositories[project]
		if repo == "" {
			fmt.Fprintln(stderr, "a repository URL is required to resolve a branch; alternatively pass the full commit")
			return 2
		}
		resolve, cancel := context.WithTimeout(ctx, 30*time.Second)
		raw, e := exec.CommandContext(resolve, "git", "ls-remote", "--exit-code", repo, "refs/heads/"+ref).Output()
		cancel()
		fields := strings.Fields(string(raw))
		if e != nil || len(fields) != 2 {
			fmt.Fprintln(stderr, "cannot resolve branch to one commit")
			return 69
		}
		commit = fields[0]
	}
	if _, err := rq.Key(project, commit, groups); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	request := rq.Request{Resource: def.ID, Project: project, Commit: commit, Groups: groups, ID: nodeclient.NewAttempt(), Holder: rq.Holder{PID: os.Getpid(), Label: project + " " + commit[:12] + " " + strings.Join(groups, ",")}}
	var reply rq.Reply
	if err := resourceCall(ctx, "resource_acquire", request, &reply); err != nil {
		fmt.Fprintln(stderr, "gate admission failed:", err)
		return 69
	}
	fmt.Fprintln(stderr, "RESOURCE_STATUS="+reply.Status)
	if reply.Position > 0 {
		fmt.Fprintf(stderr, "RESOURCE_POSITION=%d\n", reply.Position)
	}
	if reply.Status == "cached" {
		return printGateOutcome(reply.Lease, stdout, stderr)
	}
	if reply.Lease == nil {
		fmt.Fprintln(stderr, "gate refused")
		return 75
	}
	lease := reply.Lease
	joined := reply.Status == "joined"
	request.ID = lease.ID
	// Waiting never resubmits: a lost response is uncertainty, not permission to start another job.
	lastQueuedRenew := time.Now()
	for joined || lease.State == "queued" {
		if !gatePause(ctx, 2*time.Second) {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fmt.Fprintln(stderr, "RESOURCE_STATUS=timeout")
				return 75
			}
			return 130
		}
		until := lease.HoldUntil
		if lease.State == "queued" {
			until = lease.WaitUntil
		}
		if !until.IsZero() && !time.Now().Before(until) {
			fmt.Fprintln(stderr, "RESOURCE_STATUS=timeout")
			return 75
		}
		if !joined && time.Since(lastQueuedRenew) >= 20*time.Second {
			var renewed rq.Reply
			if err := resourceCall(ctx, "resource_renew", request, &renewed); err == nil {
				lastQueuedRenew = time.Now()
			}
		}
		var next rq.Reply
		if err := resourceCall(ctx, "resource_get", request, &next); err != nil {
			fmt.Fprintln(stderr, "gate status temporarily unavailable")
			continue
		}
		if next.Lease == nil {
			return 75
		}
		lease = next.Lease
		if lease.State == "released" {
			return printGateOutcome(lease, stdout, stderr)
		}
		if lease.State != "queued" && lease.State != "granted" {
			fmt.Fprintln(stderr, "gate lease ended with unknown outcome:", lease.State)
			return 75
		}
		if !joined && lease.State == "granted" {
			break
		}
	}
	request.Token = lease.Token
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		last := time.Now()
		for gatePause(run, 20*time.Second) {
			var rep rq.Reply
			if err := resourceCall(run, "resource_renew", request, &rep); err == nil {
				if rep.Status == "fenced" {
					cancel()
					return
				}
				last = time.Now()
			}
			if time.Since(last) > 60*time.Second {
				cancel()
				return
			}
		}
	}()
	// A resolved branch is submitted as its immutable commit. Bundles retain their original transport.
	submitRef := commit
	if ref == "bundle" {
		submitRef = "bundle"
	}
	words := append([]string{project, submitRef}, groups...)
	if snap.Queue != nil && !snap.Unknown {
		words = append(words, "--tag", lease.ID)
	}
	var capture gateCapture
	code := gateSSH(run, def.Mirror, words, input, io.MultiWriter(stdout, &capture), stderr)
	cancel()
	<-renewDone
	outcome := &rq.Outcome{Exit: code, Run: capture.result(), Output: string(capture.data)}
	request.Outcome = outcome
	release, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	var released rq.Reply
	if err := resourceCall(release, "resource_release", request, &released); err != nil {
		fmt.Fprintln(stderr, "gate result could not be recorded; the lease will expire")
	}
	return code
}

type gateCapture struct {
	data    []byte
	pending []byte
	run     string
}

func (c *gateCapture) Write(p []byte) (int, error) {
	n := len(p)
	c.pending = append(c.pending, p...)
	for {
		line, rest, ok := bytes.Cut(c.pending, []byte("\n"))
		if !ok {
			break
		}
		if bytes.HasPrefix(line, []byte("RESULT=")) {
			c.run = strings.TrimSpace(string(line[len("RESULT="):]))
		}
		c.pending = rest
	}
	if len(c.pending) > 8192 {
		c.pending = bytes.Clone(c.pending[len(c.pending)-8192:])
	}
	c.data = append(c.data, p...)
	if len(c.data) > 256<<10 {
		c.data = bytes.Clone(c.data[len(c.data)-(256<<10):])
	}
	return n, nil
}
func (c *gateCapture) result() string {
	if c.run != "" {
		return c.run
	}
	for _, line := range strings.Split(string(c.data), "\n") {
		if strings.HasPrefix(line, "RESULT=") {
			return strings.TrimPrefix(line, "RESULT=")
		}
	}
	return ""
}
func gatePause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func gateSSH(ctx context.Context, m rq.Mirror, words []string, input io.Reader, out, errout io.Writer) int {
	err := rq.SSH(ctx, m, words, input, out, errout)
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	fmt.Fprintln(errout, "runner unavailable")
	return 255
}
func printGateOutcome(l *rq.Lease, out, errout io.Writer) int {
	if l == nil || l.Outcome == nil {
		fmt.Fprintln(errout, "runner outcome unknown")
		return 75
	}
	o := l.Outcome
	if o.Output != "" {
		fmt.Fprint(out, o.Output)
		if !strings.Contains(o.Output, "RESULT="+o.Run) {
			fmt.Fprintln(out, "RESULT="+o.Run)
		}
		if !strings.Contains(o.Output, fmt.Sprintf("EXIT_CODE=%d", o.Exit)) {
			fmt.Fprintf(out, "EXIT_CODE=%d\n", o.Exit)
		}
	} else {
		fmt.Fprintln(out, "RESULT="+o.Run)
		fmt.Fprintf(out, "EXIT_CODE=%d\n", o.Exit)
	}
	return o.Exit
}
