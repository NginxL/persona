// Package outboxcli gives the owner terminal access to Agent messages the manager is still holding.
package outboxcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
)

const Usage = "  personad outbox list|retry|discard  inspect and recover held Agent messages (personad outbox -h)"

const (
	tokenEnv         = "PERSONA_OUTBOX_TOKEN"
	maxTokenBytes    = 4096
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
		if !validID(id) {
			return clitext.UsageError(i18n.T(lang, "outbox.invalid_id"))
		}
	}
	if command == "list" && (!devicewire.HeldOutboxStateValid(state) ||
		pageSize < 1 || pageSize > devicewire.HeldOutboxMaxPage || session != "" && !validID(session)) {
		return clitext.UsageError(i18n.T(lang, "outbox.invalid_page"))
	}
	base, err := hubURL(*hub)
	if err != nil {
		return clitext.UsageError(i18n.T(lang, "outbox.invalid_hub"))
	}
	token, err := tokenValue(*tokenFile)
	if err != nil {
		return errors.New(i18n.T(lang, "outbox.token"))
	}
	request := client{base: base, token: token, lang: lang, http: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
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

func validID(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsFunc(s, unicode.IsControl)
}

func hubURL(raw string) (*url.URL, error) {
	addr, err := url.Parse(raw)
	if err != nil || addr.Hostname() == "" || addr.User != nil || addr.RawQuery != "" ||
		addr.ForceQuery || addr.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("invalid hub URL")
	}
	switch addr.Scheme {
	case "https":
		return addr, nil
	case "http":
		host := addr.Hostname()
		if strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
			return addr, nil
		}
	}
	return nil, errors.New("invalid hub scheme or host")
}

func tokenValue(path string) (string, error) {
	raw := os.Getenv(tokenEnv)
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxTokenBytes+1))
		if err != nil {
			return "", err
		}
		raw = string(data)
	}
	token := strings.TrimSpace(raw)
	if len(raw) > maxTokenBytes || token == "" ||
		strings.ContainsFunc(token, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return "", errors.New("invalid token")
	}
	return token, nil
}

type client struct {
	base  *url.URL
	token string
	lang  i18n.Lang
	http  *http.Client
}

// endpoint keeps any hub mount prefix and quotes each ID as one path segment.
func (c client) endpoint(path string, query url.Values) string {
	base := *c.base
	base.RawPath = strings.TrimRight(base.EscapedPath(), "/") + path
	base.Path, _ = url.PathUnescape(base.RawPath)
	base.RawQuery = query.Encode()
	return base.String()
}

func (c client) getJSON(ctx context.Context, method, path string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", string(c.lang))
	resp, err := c.http.Do(req)
	if err != nil {
		if method != http.MethodGet {
			return errors.New(i18n.T(c.lang, "outbox.action_unknown"))
		}
		return errors.New(i18n.T(c.lang, "outbox.read_failed"))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return c.badResponse(method)
	}
	if resp.StatusCode != http.StatusOK {
		detail := apierr.Describe(body)
		if detail == "" {
			if method == http.MethodGet && resp.StatusCode == http.StatusNotFound {
				return errors.New(i18n.T(c.lang, "outbox.missing"))
			}
			detail = resp.Status
		}
		detail = clitext.Inert(strings.ReplaceAll(detail, c.token, "[redacted]"))
		message := i18n.T(c.lang, "outbox.http_error", i18n.Args{"status": resp.StatusCode, "detail": detail})
		if method != http.MethodGet && resp.StatusCode >= http.StatusInternalServerError {
			message += " " + i18n.T(c.lang, "outbox.action_unknown")
		}
		return errors.New(message)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return c.badResponse(method)
	}
	return nil
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
