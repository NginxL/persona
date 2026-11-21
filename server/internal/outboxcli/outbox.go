// Package outboxcli gives the owner terminal access to Agent messages the manager is still holding.
package outboxcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/hubclient"
	"github.com/nwverse/persona/server/internal/i18n"
)

const Usage = "  personad outbox list|retry|discard  inspect and recover held Agent messages (personad outbox -h)"

const (
	tokenEnv         = "PERSONA_OUTBOX_TOKEN"
	maxResponseBytes = 2 << 20
)

// Run uses the same single-message actions as the apps. It never retries a failed HTTP action.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	lang := i18n.Terminal()
	usage := i18n.T(lang, "outbox.usage", i18n.Args{"command": clitext.Named("personad outbox")})
	if len(args) > 0 && (clitext.IsHelpArg(args[0]) || args[0] == "help") {
		_, err := fmt.Fprintln(stdout, usage)
		return err
	}
	if len(args) == 0 {
		return clitext.UsageError(usage)
	}

	command := args[0]
	var targetCount int
	switch command {
	case "list":
		targetCount = 1
	case "retry", "discard":
		targetCount = 3
	default:
		return clitext.UsageError(usage)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	hub := flags.String("hub", "", "")
	tokenFile := flags.String("token-file", "", "")
	var session, state, after string
	var pageSize int
	var asJSON bool
	if command == "list" {
		flags.StringVar(&session, "session", "", "")
		flags.StringVar(&state, "state", "", "")
		flags.StringVar(&after, "after", "", "")
		flags.IntVar(&pageSize, "limit", devicewire.HeldOutboxPageSize, "")
		flags.BoolVar(&asJSON, "json", false, "")
	}
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(stdout, usage)
		return err
	} else if err != nil || flags.NArg() != targetCount {
		return clitext.UsageError(usage)
	}
	target := flags.Args()
	for _, id := range target {
		if !hubclient.ValidID(id) {
			return clitext.UsageError(i18n.T(lang, "outbox.invalid_id"))
		}
	}
	if command == "list" && (!devicewire.HeldOutboxStateValid(state) ||
		pageSize < 1 || pageSize > devicewire.HeldOutboxMaxPage || session != "" && !hubclient.ValidID(session)) {
		return clitext.UsageError(i18n.T(lang, "outbox.invalid_page"))
	}
	base, err := hubclient.BaseURL(*hub)
	if err != nil {
		return clitext.UsageError(i18n.T(lang, "outbox.invalid_hub"))
	}
	token, err := hubclient.Token(*tokenFile, tokenEnv)
	if err != nil {
		return errors.New(i18n.T(lang, "outbox.token"))
	}
	request := client{hub: hubclient.New(base, token, lang, 20*time.Second), lang: lang}
	if command == "list" {
		query := url.Values{"limit": {strconv.Itoa(pageSize)}}
		for key, value := range map[string]string{"session": session, "state": state, "after": after} {
			if value != "" {
				query.Set(key, value)
			}
		}
		return request.list(ctx, stdout, target[0], query, asJSON)
	}
	return request.act(ctx, stdout, command, target)
}

type client struct {
	hub  *hubclient.Client
	lang i18n.Lang
}

func (c client) getJSON(ctx context.Context, method, path string, query url.Values, out any) error {
	_, err := c.hub.JSON(ctx, method, path, query, maxResponseBytes, out)
	var failure *hubclient.HTTPError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &failure):
		if method == http.MethodGet && failure.Status == http.StatusNotFound && !failure.Named {
			return errors.New(i18n.T(c.lang, "outbox.missing"))
		}
		message := i18n.T(c.lang, "outbox.http_error", i18n.Args{"status": failure.Status, "detail": failure.Detail})
		if method != http.MethodGet && failure.Status >= http.StatusInternalServerError {
			message += " " + i18n.T(c.lang, "outbox.action_unknown")
		}
		return errors.New(message)
	case method == http.MethodGet && errors.Is(err, hubclient.ErrTransport):
		return errors.New(i18n.T(c.lang, "outbox.read_failed"))
	default:
		return c.badResponse(method)
	}
}

func (c client) badResponse(method string) error {
	if method != http.MethodGet {
		return errors.New(i18n.T(c.lang, "outbox.action_unknown"))
	}
	return errors.New(i18n.T(c.lang, "outbox.bad_response"))
}

func (c client) list(ctx context.Context, stdout io.Writer, machine string, query url.Values, asJSON bool) error {
	var page devicewire.HeldOutboxPage
	path := "/api/v1/machines/" + url.PathEscape(machine) + "/held-messages"
	if err := c.getJSON(ctx, http.MethodGet, path, query, &page); err != nil {
		return err
	}
	if page.MachineID == "" || page.Messages == nil {
		return c.badResponse(http.MethodGet)
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(page)
	}
	var report strings.Builder
	fmt.Fprintln(&report, i18n.T(c.lang, "outbox.heading", i18n.Args{
		"name": clitext.Inert(page.Computer), "id": clitext.Inert(page.MachineID),
	}))
	if len(page.Messages) == 0 {
		fmt.Fprintln(&report, i18n.T(c.lang, "outbox.empty"))
	}
	for _, row := range page.Messages {
		fmt.Fprintln(&report, i18n.T(c.lang, "outbox.row", i18n.Args{
			"state": c.stateLabel(row.State), "session": clitext.Inert(row.SessionID),
			"id": clitext.Inert(row.ID), "time": row.SentAt.UTC().Format(time.RFC3339),
		}))
		fmt.Fprintln(&report, "  "+clitext.Inert(row.Detail))
		if row.CanRetry {
			fmt.Fprintln(&report, "  "+i18n.T(c.lang, "outbox.actions_retry_discard"))
		} else if row.CanDiscard {
			fmt.Fprintln(&report, "  "+i18n.T(c.lang, "outbox.action_discard"))
		}
	}
	if page.NextCursor != "" {
		fmt.Fprintln(&report, i18n.T(c.lang, "outbox.more", i18n.Args{"cursor": clitext.Inert(page.NextCursor)}))
	}
	_, err := io.WriteString(stdout, report.String())
	return err
}

func (c client) stateLabel(state string) string {
	if state != "" && devicewire.HeldOutboxStateValid(state) {
		return i18n.T(c.lang, "outbox.state_"+state)
	}
	return i18n.T(c.lang, "outbox.state_unknown")
}

func (c client) act(ctx context.Context, stdout io.Writer, command string, ids []string) error {
	path := "/api/v1/sessions/" + url.PathEscape(ids[0]) + "/" + url.PathEscape(ids[1]) + "/held/" + url.PathEscape(ids[2])
	method, message := http.MethodDelete, "outbox.discarded"
	if command == "retry" {
		method, message = http.MethodPost, "outbox.retried"
		path += "/retry"
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := c.getJSON(ctx, method, path, nil, &result); err != nil {
		return err
	}
	if !result.OK {
		return c.badResponse(method)
	}
	_, err := fmt.Fprintln(stdout, i18n.T(c.lang, message))
	return err
}
