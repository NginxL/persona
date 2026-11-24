package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's writes (prompts/mcp/github): they go through the hub's gate, by default the user allows each one
// on the phone, so Describe writes the card: where, what, who will see it.

var actionTools = []string{"github_comment", "github_review"}

func (c *Connector) Actions() []proto.Tool {
	out := make([]proto.Tool, 0, len(actionTools))
	for _, n := range actionTools {
		t := prompts.Tool(n)
		out = append(out, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

type writeArgs struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Body   string `json:"body"`
	Event  string `json:"event"`
	// The thread page's reply (POST /api/v1/connectors/github/thread/reply): {chat: "owner/repo#12", text}.
	Chat string `json:"chat"`
	Text string `json:"text"`
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func (a writeArgs) ref() string { return fmt.Sprintf("%s#%d", a.Repo, a.Number) }

func parseWrite(name string, args json.RawMessage) (writeArgs, error) {
	var a writeArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, fmt.Errorf("invalid arguments: %v", err)
	}
	if m := threadRef.FindStringSubmatch(a.Chat); m != nil && a.Repo == "" && a.Number == 0 {
		a.Repo, a.Number = m[1], atoi(m[2])
	}
	if a.Body == "" {
		a.Body = a.Text
	}
	a.Body = strings.TrimSpace(a.Body)
	switch {
	case !validRepo(a.Repo) || a.Number <= 0:
		return a, fmt.Errorf("repo must be owner/name and number a PR / issue number")
	case name == "github_comment" && a.Body == "":
		return a, fmt.Errorf("body is required")
	case name == "github_review" && a.Event != "APPROVE" && a.Event != "REQUEST_CHANGES" && a.Event != "COMMENT":
		return a, fmt.Errorf("event must be APPROVE, REQUEST_CHANGES or COMMENT")
	case name == "github_review" && a.Event != "APPROVE" && a.Body == "":
		return a, fmt.Errorf("body is required to %s", strings.ToLower(strings.ReplaceAll(a.Event, "_", " ")))
	}
	return a, nil
}

// Describe is the phone's card: the action, the PR / issue (with its title when GitHub answers quickly), the full
// text. Arguments it cannot read are listed as they are.
func (c *Connector) Describe(ctx context.Context, name string, args json.RawMessage) (connectorwire.Action, string, error) {
	a, err := parseWrite(name, args)
	if err != nil {
		return connectorwire.Action{Name: name}, name + " " + string(args), nil
	}
	where := a.ref()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var it struct {
		Title string `json:"title"`
	}
	if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/issues/%d", a.Repo, a.Number), &it); err == nil && it.Title != "" {
		where += "「" + it.Title + "」"
	}
	act := connectorwire.Action{Name: name, Visible: a.Repo + " 仓库能看到的所有人（公开仓库是所有人）"}
	var head string
	switch {
	case name == "github_comment":
		head = "在 GitHub 上评论 " + where
	case a.Event == "APPROVE":
		head = "【批准】在 GitHub 上批准（approve）这个 PR：" + where
	case a.Event == "REQUEST_CHANGES":
		head = "在 GitHub 上要求修改（request changes）" + where
	default:
		head = "在 GitHub 上评审（comment）" + where
	}
	if a.Body == "" {
		return act, head, nil
	}
	return act, head + "：\n\n" + a.Body, nil
}

// Do posts it (only once the gate let it through).
func (c *Connector) Do(ctx context.Context, name string, args json.RawMessage) (string, error) {
	done, err := c.DoRef(ctx, name, args)
	return done.Text, err
}

var _ connectorwire.RefActions = (*Connector)(nil)

// DoRef is Do with the id GitHub gave what it posted (connectorwire.RefActions): the comment's id for github_comment, the
// review's for github_review, as GitHub's answer has it, in decimal. A comment and a review are different resources
// with numberings of their own, so the id alone does not say which: the action does.
func (c *Connector) DoRef(ctx context.Context, name string, args json.RawMessage) (connectorwire.DoResult, error) {
	a, err := parseWrite(name, args)
	if err != nil {
		return connectorwire.DoResult{}, err
	}
	// The id is read after the post went out and must never turn it into a failure (the model would post it again), so
	// it is taken as it comes and read below, whatever it is.
	var out struct {
		ID      json.RawMessage `json:"id"`
		HTMLURL string          `json:"html_url"`
	}
	var text string
	switch name {
	case "github_comment":
		body, _ := json.Marshal(map[string]string{"body": a.Body})
		if _, _, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", a.Repo, a.Number), nil, body, &out); err != nil {
			return connectorwire.DoResult{}, err
		}
		c.forget(a.Repo, a.Number)
		text = "Commented on " + a.ref() + ": " + out.HTMLURL
	case "github_review":
		req := map[string]string{"event": a.Event}
		if a.Body != "" {
			req["body"] = a.Body
		}
		body, _ := json.Marshal(req)
		if _, _, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/reviews", a.Repo, a.Number), nil, body, &out); err != nil {
			return connectorwire.DoResult{}, err
		}
		c.forget(a.Repo, a.Number)
		text = "Review (" + a.Event + ") submitted on " + a.ref() + ": " + out.HTMLURL
	default:
		return connectorwire.DoResult{}, fmt.Errorf("unknown tool %s", name)
	}
	return connectorwire.DoResult{Text: text, MessageRef: wholeID(out.ID)}, nil
}

// wholeID is an id GitHub answered as a positive whole number, in decimal; "" for anything else (none, a string, a
// fraction, an object, one past int64): no message ref, not an error.
func wholeID(raw json.RawMessage) string {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}
