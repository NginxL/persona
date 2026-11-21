package scriptcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

const decisionUsage = `usage: personad decision raise --file decision.json
       personad decision raise --file - < decision.json
       personad decision get <decision-id>
       personad decision withdraw <decision-id>
  raise returns a durable id without waiting; get and withdraw print one JSON result
  exit: 0 success (including pending), 1 not found / state conflict, 2 usage, 4 unavailable, 130 interrupted`

const decisionDocumentMost = 64 << 10

// Decision runs `personad decision` and returns its exit code.
func Decision(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return Default().Decision(ctx, args, stdin, stdout, stderr)
}

// Decision raises, polls or withdraws a rich owner decision through the local
// node. It has no wait mode and no terminal fallback: scripts choose their own
// polling and no-answer rules.
func (r Runner) Decision(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stdout, decisionUsage)
		return ExitOK
	}
	var reply assistantwire.OutsideDecisionReply
	var err error
	pane := nodeclient.HookPane(r.Getenv)
	switch args[0] {
	case "raise":
		fs := newFlags("decision raise")
		file := fs.String("file", "", "")
		if parseErr := fs.Parse(args[1:]); errors.Is(parseErr, flag.ErrHelp) {
			fmt.Fprintln(stdout, decisionUsage)
			return ExitOK
		} else if parseErr != nil || *file == "" || fs.NArg() != 0 {
			fmt.Fprintf(stderr, "personad decision raise: --file is required and takes no positional arguments\n%s\n", decisionUsage)
			return ExitUsage
		}
		var raw []byte
		if *file == "-" {
			raw, err = io.ReadAll(io.LimitReader(stdin, decisionDocumentMost+1))
		} else {
			read := r.ReadFile
			if read == nil {
				read = os.ReadFile
			}
			raw, err = read(*file)
		}
		if err == nil && len(raw) > decisionDocumentMost {
			err = fmt.Errorf("the decision document is longer than %d bytes", decisionDocumentMost)
		}
		var in assistantwire.OutsideDecisionRequest
		if err == nil {
			err = json.Unmarshal(raw, &in)
		}
		if err != nil {
			fmt.Fprintf(stderr, "personad decision raise: %s\n", i18n.T(i18n.Terminal(), "scriptcli-decision.invalid", i18n.Args{"reason": err.Error()}))
			return ExitUsage
		}
		cwd, _ := r.Getwd()
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		reply, err = nodeclient.DecisionRaiseNode(ctx, r.Dial, nodeclient.DecisionRaiseRequest{Decision: in, Cwd: cwd}, pane)
	case "get", "withdraw":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			fmt.Fprintf(stderr, "personad decision %s: exactly one decision id is required\n%s\n", args[0], decisionUsage)
			return ExitUsage
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if args[0] == "get" {
			reply, err = nodeclient.DecisionGetNode(ctx, r.Dial, args[1], pane)
		} else {
			reply, err = nodeclient.DecisionWithdrawNode(ctx, r.Dial, args[1], pane)
		}
	default:
		fmt.Fprintf(stderr, "personad decision: unknown action %q\n%s\n", args[0], decisionUsage)
		return ExitUsage
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ExitInterrupted
	}
	lang := i18n.Terminal()
	if err != nil {
		reason := err.Error()
		if errors.Is(err, nodeclient.ErrNoNode) {
			reason = i18n.T(lang, "scriptcli-ask.no_node")
		}
		fmt.Fprintf(stderr, "personad decision: %s\n", i18n.T(lang, "scriptcli-decision.unavailable", i18n.Args{"reason": reason}))
		return ExitUnreachable
	}
	if reply.Decision == nil {
		if reply.Code == "" {
			reply.Code, reply.Error = "unavailable", i18n.T(lang, "scriptcli-decision.too_old")
		}
		fmt.Fprintf(stderr, "personad decision: %s\n", reply.Error)
		switch reply.Code {
		case "invalid_decision", "invalid_todo", "extension_unknown":
			return ExitUsage
		case "decision_not_found", "decision_closed", "decision_ref_conflict", "todo_not_found", "todo_closed", "forbidden":
			return ExitDenied
		default:
			return ExitUnreachable
		}
	}
	if err := json.NewEncoder(stdout).Encode(reply); err != nil {
		fmt.Fprintf(stderr, "personad decision: %v\n", err)
		return ExitUnreachable
	}
	return ExitOK
}
