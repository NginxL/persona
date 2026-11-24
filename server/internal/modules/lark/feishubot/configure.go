package feishubot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// Configuring the app through the open platform's server API (docs/extensions/bots.md "What to do on the Open Platform"),
// so that after the credentials verify, everything the console asked by hand — scopes, events, the long
// connection, the publish — happens by itself. Best-effort: nothing here blocks binding; a private domain
// (an unknown console) skips it, and every failure degrades to one sentence and a link to the console.
//
// Both endpoints need the app's own tenant token and the `application:application:patch` scope, which the
// platform does not open by itself: that one switch is the only manual step left besides creating the app.

// consoleURL is the developer console for this client's cloud (ConsoleURL); c.console overrides the cloud the domain
// says (tests).
func (c *Client) consoleURL() string {
	if c.console != "" {
		return c.console
	}
	return ConsoleURL(c.domain)
}

// The scopes and events the bot needs (docs/extensions/bots.md "What to do on the Open Platform"). Group messaging is
// deliberately not opened here: im:message.group_msg is sensitive and usually needs the enterprise admin's
// review, and the group gate works without it.
var configureScopes = []string{"im:message:send_as_bot", "contact:user.base:readonly", "cardkit:card:write"}
var configureEvents = []string{"im.message.receive_v1", "card.action.trigger", "p2p_chat_create", "im.message.recalled_v1", EventP2PEntered}

// AutoConfigure opens the scopes and events with the long-connection subscription mode, then submits the
// publish (which creates the version when none is pending). It runs on the freshly saved credentials; the
// caller shows the report on the bind step and never fails the bind on it.
func (c *Client) AutoConfigure(ctx context.Context) ConfigureReport {
	if c.appID == "" {
		return ConfigureReport{Skipped: true, Issue: IssueNoApp}
	}
	if c.consoleURL() == "" {
		return ConfigureReport{Skipped: true} // a private deployment: its console is not ours to configure
	}

	var rep ConfigureReport
	body := map[string]any{
		"scope": map[string]any{
			"add_scopes": scopesPayload(configureScopes),
		},
		"event": map[string]any{
			"subscription_type": "websocket",
			"add_events":        configureEvents,
		},
	}
	if err := c.call(ctx, "configure", http.MethodPatch, "/open-apis/application/v7/applications/"+c.appID+"/config", body, nil); err != nil {
		rep.Issue, rep.Detail = configureIssue(err)
		return rep
	}
	rep.Configured = true

	pub := map[string]any{"remark": "Persona bot", "changelog": "Permissions, events and the long-connection subscription for the Persona bot"}
	var pubOut struct {
		VersionID string `json:"version_id"`
	}
	if err := c.call(ctx, "publish", http.MethodPost, "/open-apis/application/v7/applications/"+c.appID+"/publish", pub, &pubOut); err != nil {
		rep.Issue, rep.Detail = configureIssue(err)
		if rep.Issue == IssueUnderReview {
			rep.Published, rep.Issue, rep.Detail = true, IssueNone, "" // already submitted: nothing to do
		}
		return rep
	}
	rep.Published = true
	rep.VersionID = pubOut.VersionID
	return rep
}

func scopesPayload(names []string) []map[string]string {
	out := make([]map[string]string, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]string{"scope_name": n, "token_type": "tenant"})
	}
	return out
}

// configureIssue maps an API error to the sentence the app shows. A permission denial on either call is the
// patch scope missing (nothing else on these endpoints denies by scope), unless the platform said otherwise.
func configureIssue(err error) (ConfigureIssue, string) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return IssueUnreachable, err.Error()
	}
	switch apiErr.Code {
	case 210041:
		return IssueBotCapability, apiErr.Msg
	case 210001, 210036:
		return IssueNotSelfBuilt, apiErr.Msg
	case 210302:
		return IssueUnderReview, apiErr.Msg
	case 210031, 210032, 210034:
		return IssueIncompatible, apiErr.Msg
	case 230002, 230003: // forbidden / not authorized, as the config API answers it
		return IssuePatchScope, apiErr.Msg
	}
	return IssueUnreachable, fmt.Sprintf("%d %s", apiErr.Code, apiErr.Msg)
}
