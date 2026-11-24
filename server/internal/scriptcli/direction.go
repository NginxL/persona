package scriptcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

const directionCallTimeout = 30 * time.Second

const (
	createDirectionUsage = "scriptcli-direction.create_usage"
	writeProgressUsage   = "scriptcli-direction.write_usage"
)

// CreateDirection runs `personad create-direction` and returns its exit code.
func CreateDirection(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return Default().CreateDirection(ctx, args, stdout, stderr)
}

// WriteProgress runs `personad write-progress` and returns its exit code.
func WriteProgress(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return Default().WriteProgress(ctx, args, stdout, stderr)
}

// CreateDirection creates a direction through one local node.sock request. An explicitly supplied machine and session
// are a pair; nothing is inferred from the caller's environment or working directory.
func (r Runner) CreateDirection(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	lang := i18n.Terminal()
	fs := newFlags("create-direction")
	project := fs.String("project", "", "")
	machine := fs.String("machine", "", "")
	session := fs.String("session", "", "")
	pos, err := split(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, i18n.T(lang, createDirectionUsage))
		return ExitOK
	}
	if err != nil {
		return directionUsageError(stderr, lang, "create-direction", createDirectionUsage, "invalid_flags", i18n.Args{"reason": err.Error()})
	}
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
		return directionUsageError(stderr, lang, "create-direction", createDirectionUsage, "name_required", nil)
	}
	if strings.TrimSpace(*project) == "" {
		return directionUsageError(stderr, lang, "create-direction", createDirectionUsage, "project_required", nil)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["machine"] != set["session"] || set["machine"] && (strings.TrimSpace(*machine) == "" || strings.TrimSpace(*session) == "") {
		return directionUsageError(stderr, lang, "create-direction", createDirectionUsage, "agent_pair", nil)
	}
	req := nodeclient.DirectionCreateRequest{Name: pos[0], Project: *project, MachineID: *machine, SessionID: *session}
	return r.directionCall(ctx, "create-direction", stdout, stderr, func(ctx context.Context) (nodeclient.DirectionReply, error) {
		return nodeclient.CreateDirectionNode(ctx, r.Dial, req)
	})
}

// WriteProgress replaces a direction's progress text through one local node.sock request. Links alone are not a
// progress report; at least one text field must be non-empty. Omitted text fields are sent as empty strings.
func (r Runner) WriteProgress(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	lang := i18n.Terminal()
	fs := newFlags("write-progress")
	reached := fs.String("reached", "", "")
	doing := fs.String("doing", "", "")
	needsYou := fs.String("needs-you", "", "")
	var links directionLinks
	fs.Var(&links, "link", "")
	pos, err := split(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, i18n.T(lang, writeProgressUsage))
		return ExitOK
	}
	if err != nil {
		return directionUsageError(stderr, lang, "write-progress", writeProgressUsage, "invalid_flags", i18n.Args{"reason": err.Error()})
	}
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
		return directionUsageError(stderr, lang, "write-progress", writeProgressUsage, "id_required", nil)
	}
	if strings.TrimSpace(*reached) == "" && strings.TrimSpace(*doing) == "" && strings.TrimSpace(*needsYou) == "" {
		return directionUsageError(stderr, lang, "write-progress", writeProgressUsage, "progress_required", nil)
	}
	req := nodeclient.DirectionProgressRequest{ID: pos[0], Reached: *reached, Doing: *doing, NeedsYou: *needsYou, Links: links}
	return r.directionCall(ctx, "write-progress", stdout, stderr, func(ctx context.Context) (nodeclient.DirectionReply, error) {
		return nodeclient.WriteDirectionProgressNode(ctx, r.Dial, req)
	})
}

// directionLinks collects repeated --link values verbatim: commas are ordinary URL characters, not separators.
type directionLinks []string

func (l *directionLinks) String() string { return strings.Join(*l, ",") }
func (l *directionLinks) Set(value string) error {
	*l = append(*l, value)
	return nil
}

func directionUsageError(stderr io.Writer, lang i18n.Lang, command, usage, message string, args i18n.Args) int {
	fmt.Fprintf(stderr, "personad %s: %s\n%s\n", command, i18n.T(lang, "scriptcli-direction."+message, args), i18n.T(lang, usage))
	return ExitUsage
}

// directionCall bounds one write and maps the shared reply to the script CLI's streams and exit codes. In particular,
// it has no reconnect loop: after an uncertain transport result the caller decides whether to write again.
func (r Runner) directionCall(ctx context.Context, command string, stdout, stderr io.Writer, call func(context.Context) (nodeclient.DirectionReply, error)) int {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, directionCallTimeout)
	defer cancel()
	rep, err := call(ctx)
	if parent.Err() != nil {
		return ExitInterrupted
	}
	if err != nil {
		why := err.Error()
		if errors.Is(err, nodeclient.ErrNoNode) {
			why = i18n.T(i18n.Terminal(), "scriptcli-notify.no_node")
		}
		fmt.Fprintf(stderr, "personad %s: %s\n", command, why)
		return ExitUnreachable
	}
	switch rep.Status {
	case nodeclient.DirectionOK:
		out, err := json.Marshal(struct {
			Leader json.RawMessage `json:"leader"`
		}{Leader: rep.Leader})
		if err != nil { // directionNode has already required an object; keep stdout machine-readable if that changes.
			fmt.Fprintf(stderr, "personad %s: %v\n", command, err)
			return ExitUnreachable
		}
		_, _ = stdout.Write(append(out, '\n'))
		return ExitOK
	case nodeclient.DirectionInvalid:
		directionReplyError(stderr, command, rep)
		return ExitUsage
	case nodeclient.DirectionFailed:
		directionReplyError(stderr, command, rep)
		return ExitDenied
	default:
		directionReplyError(stderr, command, rep)
		return ExitUnreachable
	}
}

func directionReplyError(stderr io.Writer, command string, rep nodeclient.DirectionReply) {
	why := rep.Error
	if why == "" {
		why = rep.Status
	}
	fmt.Fprintf(stderr, "personad %s: %s\n", command, why)
}
