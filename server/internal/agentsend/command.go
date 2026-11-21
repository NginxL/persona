// Package agentsend lets scripts send one message and recover its hub receipt.
package agentsend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/hubclient"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const Usage = "  personad agent send|receipt …  send a message and recover its receipt (personad agent -h)"

const (
	maxMessageBytes = devicewire.MaxTerminalAgentMessageBytes
	maxReplyBytes   = 2 << 20
	defaultTimeout  = 30 * time.Second
	maxTimeout      = 5 * time.Minute
	holdFeature     = "agent_message_hold"
)

var (
	// ErrUnconfirmed means the command printed the outcome and exits nonzero.
	ErrUnconfirmed = errors.New("agent delivery unconfirmed")
	uuid           = lazyre.New(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type options struct {
	command                   string
	hub, tokenFile            string
	machine, session, message string
	id                        string
	timeout                   time.Duration
	hold, wait, json          bool
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	lang := i18n.Terminal()
	usage := i18n.T(lang, "agent-send.usage", i18n.Args{"command": clitext.Named("personad agent")})
	opts, err := parseOptions(args, lang, usage)
	if errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(stdout, usage)
		return err
	}
	if err != nil {
		return err
	}
	base, err := hubclient.BaseURL(opts.hub)
	if err != nil {
		return clitext.UsageError(i18n.T(lang, "agent-send.invalid_hub"))
	}
	token, err := hubclient.Token(opts.tokenFile, "")
	if err != nil {
		return errors.New(i18n.T(lang, "agent-send.token"))
	}
	var text string
	if opts.command == "send" {
		text, err = readMessage(opts.message, stdin)
		if err != nil {
			return clitext.UsageError(i18n.T(lang, "agent-send.message"))
		}
		if opts.id == "" {
			opts.id = newID()
		}
	}
	var features []string
	if opts.hold {
		features = append(features, holdFeature)
	}
	requestCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	client := hubclient.New(base, token, lang, opts.timeout, features...)
	if err := verifyHub(requestCtx, client, lang, opts.command == "send", opts.hold); err != nil {
		return err
	}

	var outcome result
	if opts.command == "send" {
		if err := verifyTarget(requestCtx, client, opts, lang); err != nil {
			return err
		}
		// Print the stable ID before a write can reach the hub. Failure to
		// report it means the command has not yet sent anything.
		if _, err := fmt.Fprintln(stderr, i18n.T(lang, "agent-send.dispatching", i18n.Args{"id": opts.id})); err != nil {
			return err
		}
		outcome = send(ctx, requestCtx, client, opts, text)
	} else {
		outcome = check(requestCtx, client, opts)
	}
	if err := printResult(stdout, lang, outcome, opts.json); err != nil {
		return err
	}
	if outcome.State != delivered && outcome.State != queued && outcome.State != held {
		return ErrUnconfirmed
	}
	return nil
}

func parseOptions(args []string, lang i18n.Lang, usage string) (options, error) {
	if len(args) == 1 && (clitext.IsHelpArg(args[0]) || args[0] == "help") {
		return options{}, flag.ErrHelp
	}
	if len(args) == 0 || args[0] != "send" && args[0] != "receipt" {
		return options{}, clitext.UsageError(usage)
	}
	opts := options{command: args[0]}
	flags := flag.NewFlagSet(opts.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.hub, "hub", "", "")
	flags.StringVar(&opts.tokenFile, "token-file", "", "")
	flags.DurationVar(&opts.timeout, "timeout", defaultTimeout, "")
	flags.BoolVar(&opts.json, "json", false, "")
	if opts.command == "send" {
		flags.StringVar(&opts.message, "message-file", "", "")
		flags.StringVar(&opts.id, "request-id", "", "")
		flags.BoolVar(&opts.hold, "hold", false, "")
	} else {
		flags.BoolVar(&opts.wait, "wait", false, "")
	}
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return options{}, err
	} else if err != nil || opts.hub == "" || opts.tokenFile == "" || opts.timeout <= 0 || opts.timeout > maxTimeout {
		return options{}, clitext.UsageError(usage)
	}
	if opts.command == "send" {
		if flags.NArg() != 2 || opts.message == "" {
			return options{}, clitext.UsageError(usage)
		}
		opts.machine, opts.session = flags.Arg(0), flags.Arg(1)
		if !hubclient.ValidID(opts.machine) || !hubclient.ValidID(opts.session) {
			return options{}, clitext.UsageError(i18n.T(lang, "agent-send.invalid_target"))
		}
	} else {
		if flags.NArg() != 1 {
			return options{}, clitext.UsageError(usage)
		}
		opts.id = flags.Arg(0)
	}
	if (opts.id != "" || opts.command == "receipt") && !uuid.MatchString(opts.id) {
		return options{}, clitext.UsageError(i18n.T(lang, "agent-send.invalid_id"))
	}
	opts.id = strings.ToLower(opts.id)
	return opts, nil
}

func newID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func readMessage(path string, stdin io.Reader) (string, error) {
	input := stdin
	if path != "-" {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("message source is not a regular file")
		}
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		input = f
	}
	data, err := io.ReadAll(io.LimitReader(input, maxMessageBytes+1))
	if err != nil || len(data) > maxMessageBytes || !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
		return "", errors.New("invalid message")
	}
	return string(data), nil
}

func verifyHub(ctx context.Context, client *hubclient.Client, lang i18n.Lang, sending, hold bool) error {
	var hello struct {
		Type     string   `json:"type"`
		Features []string `json:"features"`
	}
	if err := client.Hello(ctx, &hello); err != nil {
		var refusal *hubclient.HTTPError
		if errors.As(err, &refusal) {
			return errors.New(i18n.T(lang, "agent-send.http_error", i18n.Args{"status": refusal.Status}))
		}
		return errors.New(i18n.T(lang, "agent-send.connection"))
	}
	if hello.Type != "hello" || !slices.Contains(hello.Features, "client_ops") {
		return errors.New(i18n.T(lang, "agent-send.receipts_unsupported"))
	}
	if sending && !slices.Contains(hello.Features, proto.FeatureAgentTerminalSend) {
		return errors.New(i18n.T(lang, "agent-send.target_unsupported"))
	}
	if hold && !slices.Contains(hello.Features, holdFeature) {
		return errors.New(i18n.T(lang, "agent-send.hold_unsupported"))
	}
	return nil
}

func (opts options) targetPath() string {
	return "/api/v1/sessions/" + url.PathEscape(opts.machine) + "/" + url.PathEscape(opts.session) + "/agent-messages"
}

func verifyTarget(ctx context.Context, client *hubclient.Client, opts options, lang i18n.Lang) error {
	var target struct {
		Machine string `json:"machine_id"`
		Session string `json:"session_id"`
	}
	if _, err := client.JSON(ctx, http.MethodGet, opts.targetPath(), nil, maxReplyBytes, &target); err != nil {
		var refusal *hubclient.HTTPError
		if errors.As(err, &refusal) && refusal.Code == "full_device_required" {
			return errors.New(i18n.T(lang, "agent-send.full_device_required"))
		}
		return errors.New(i18n.T(lang, "agent-send.target_required"))
	}
	if target.Machine != opts.machine || target.Session != opts.session {
		return errors.New(i18n.T(lang, "agent-send.target_required"))
	}
	return nil
}

func send(ctx, requestCtx context.Context, client *hubclient.Client, opts options, text string) result {
	params := map[string]any{"text": text, "hold_when_away": opts.hold}
	payload, _ := json.Marshal(params)
	path := opts.targetPath()
	status, body, err := client.PostOperation(requestCtx, path, opts.id, payload, maxReplyBytes)
	outcome := result{ID: opts.id, State: unknown}
	if err == nil {
		outcome = interpretReply(opts.id, status, body)
	}
	if outcome.State != unknown || ctx.Err() != nil {
		return outcome
	}

	// A lost response may have left a receipt. Use a fresh read-only deadline
	// and require the exact input, since a supplied ID might already be used.
	decoded, _ := url.PathUnescape(path)
	target := devicewire.ClientOpTarget(decoded, "")
	params["client_msg_id"] = opts.id
	expected := receiptTarget{path: decoded, digest: devicewire.ClientOpDigest("message", target, true, params)}
	recoveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if recovered, present := readReceipt(recoveryCtx, client, opts.id, &expected); present {
		return recovered
	}
	return outcome
}

func printResult(stdout io.Writer, lang i18n.Lang, outcome result, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(stdout).Encode(outcome)
	}
	key := "agent-send.state_" + string(outcome.State)
	if outcome.Reason != "" {
		key = "agent-send.pending_" + outcome.Reason
	}
	message := i18n.T(lang, "agent-send.result", i18n.Args{"id": outcome.ID, "state": i18n.T(lang, key)})
	_, err := fmt.Fprintln(stdout, message)
	return err
}
