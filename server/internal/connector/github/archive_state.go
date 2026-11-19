package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
)

// A thread's state in the knowledge base (#9427, docs/assistant/knowledge.md "Chat state and finished projects"): a
// merge, close or reopen moves a thread's updated_at but adds no comment, so every chat the archive sends carries the
// thread's state, and a change of state since the one last sent adds one line to the chat, which its summary and its
// projects then follow.

// archiveState is a thread's state as the knowledge base records it (kbwire.ChatOpen, ChatMerged, ChatClosed) and
// since when (zero: unknown, an open thread never closed).
func archiveState(it issue) (string, time.Time) {
	switch {
	case it.PullRequest != nil && it.PullRequest.MergedAt != nil:
		return kbwire.ChatMerged, it.PullRequest.MergedAt.UTC()
	case it.State == "closed":
		if it.ClosedAt != nil {
			return kbwire.ChatClosed, it.ClosedAt.UTC()
		}
		return kbwire.ChatClosed, it.UpdatedAt.UTC()
	}
	return kbwire.ChatOpen, time.Time{}
}

// archiveStateLine is the line a change of state from prev ("" open, or never seen ended) adds, if any: a thread merged
// or closed, or reopened after it had ended, at the state's time (for a reopen, the thread's update). Its ID is the
// state and that time, so a replay or a cursor that starts over sends the same line, which the knowledge base keeps
// once.
func archiveStateLine(it issue, prev string) (kb.SourceMessage, bool) {
	state, at := archiveState(it)
	if state == prev {
		return kb.SourceMessage{}, false
	}
	var text string
	switch state {
	case kbwire.ChatMerged:
		text = "（状态）已合并" // i18n-ignore: archive data, as the review lines
	case kbwire.ChatClosed:
		text = "（状态）已关闭" // i18n-ignore: archive data
	default:
		if !kbwire.ChatEnded(prev) {
			return kb.SourceMessage{}, false // open, and never seen ended: nothing happened
		}
		at = it.UpdatedAt.UTC()
		text = "（状态）重新打开" // i18n-ignore: archive data
	}
	return kb.SourceMessage{ID: fmt.Sprintf("state-%s-%d", state, at.Unix()), SenderName: "GitHub", Type: "SYSTEM", Text: text, At: at}, true
}

// archiveChat is the knowledge-base chat of thread it, with its state.
func archiveChat(it issue) kb.SourceChat {
	kind := "issue"
	if it.PullRequest != nil {
		kind = "pr"
	}
	owner, name, _ := strings.Cut(it.repo(), "/")
	state, at := archiveState(it)
	return kb.SourceChat{ID: fmt.Sprintf("%s-%s-%d", owner, name, it.Number), Name: fmt.Sprintf("%s#%d %s", name, it.Number, it.Title),
		Kind: kind, UpdatedAt: it.UpdatedAt, State: state, StateAt: at}
}

// errArchiveMoved: GitHub answered with another thread than the one asked (a repository renamed or an issue moved).
var errArchiveMoved = errors.New("GitHub returned a different archive thread")

// archiveIssue reads one thread by its repository ("owner/name") and number; code is GitHub's status, h its answer's
// headers.
func (c *Connector) archiveIssue(ctx context.Context, repo string, number int) (it issue, code int, h http.Header, err error) {
	owner, name, _ := strings.Cut(repo, "/")
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(name), number)
	if code, h, err = c.request(ctx, http.MethodGet, path, nil, nil, &it); err != nil {
		return it, code, h, err
	}
	if it.Number != number || !strings.EqualFold(it.repo(), repo) || it.UpdatedAt.IsZero() {
		return it, code, h, errArchiveMoved
	}
	return it, code, h, nil
}

// archiveGone says whether a failed read of one thread is for good: GitHub answered with another thread, or refused
// it (gone, out of reach, a request it can't take) other than for a rate limit, a lapsed sign-in or a timeout, which a
// later call retries.
func archiveGone(code int, h http.Header, err error) bool {
	if errors.Is(err, errArchiveMoved) {
		return true
	}
	if code < 400 || code >= 500 || rateLimited(h, err) {
		return false
	}
	return code != http.StatusUnauthorized && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
}

// archiveRecheckQueue queues, once, the threads a cursor from before states were reported knows, so each is read once
// (archiveRecheck) and a pull request merged before then is recorded too. A cursor that knows no thread queues
// nothing and stays as it is; Changes marks it Rechecked when a call ends well.
func archiveRecheckQueue(cur *archiveCursor) {
	if cur.Rechecked || len(cur.Threads) == 0 {
		return
	}
	for t := range cur.Threads {
		cur.Recheck = append(cur.Recheck, t)
	}
	sort.Strings(cur.Recheck)
	cur.Rechecked = true
}

// noteArchiveState keeps thread's state in the cursor: only an ended one, so open threads (most) cost no room;
// "" reads as open, or never seen ended.
func noteArchiveState(cur *archiveCursor, thread string, it issue) {
	state, _ := archiveState(it)
	if !kbwire.ChatEnded(state) {
		delete(cur.States, thread)
		return
	}
	if cur.States == nil {
		cur.States = map[string]string{}
	}
	cur.States[thread] = state
}

// archiveRecheck reads the state of up to limit queued threads, one request each, and returns the chats whose state
// it sends; limited: it stopped for a rate limit, the thread still first in the queue, to be read once the limit lifts.
// A thread that can't be read for good (archiveGone, or a name the cursor holds wrongly) leaves the queue without a
// state, so it never holds the archive up; any other failure ends the call, to be tried again.
func (c *Connector) archiveRecheck(ctx context.Context, cur *archiveCursor, limit int) (chats []kb.SourceChat, lines [][]kb.SourceMessage, n int, limited bool, err error) {
	for len(cur.Recheck) > 0 && n < limit {
		if c.ratesLow() {
			return chats, lines, n, true, nil
		}
		if ctx.Err() != nil {
			break
		}
		thread := cur.Recheck[0]
		repo, num, _ := strings.Cut(thread, "#")
		number, _ := strconv.Atoi(num)
		if validArchiveRepo(repo) && number > 0 {
			n++
			it, code, h, readErr := c.archiveIssue(ctx, repo, number)
			switch {
			case readErr == nil:
				var msgs []kb.SourceMessage
				if line, ok := archiveStateLine(it, cur.States[thread]); ok {
					msgs = append(msgs, line)
				}
				chats = append(chats, archiveChat(it))
				lines = append(lines, msgs)
				noteArchiveState(cur, thread, it)
			case rateLimited(h, readErr) || c.ratesLow():
				return chats, lines, n, true, nil
			case archiveGone(code, h, readErr):
			default:
				return chats, lines, n, false, readErr
			}
		}
		cur.Recheck = cur.Recheck[1:]
	}
	return chats, lines, n, false, nil
}
