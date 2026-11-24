package github

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

const messageLastPrefix = "gh-message:"

type messageHead struct {
	At  time.Time `json:"at"`
	IDs []string  `json:"ids"`
}

type inboxMessageRecord struct {
	Last string    `json:"last"`
	Seen time.Time `json:"seen"`
}

func unmarkedLast(last string) string {
	if i := strings.LastIndexByte(last, '#'); i >= 0 {
		if _, err := strconv.ParseUint(last[i+1:], 10, 64); err == nil {
			return last[:i]
		}
	}
	return last
}

// Keep the IDs at the newest message time, including ones subsequently deleted. A timestamp alone loses same-second
// messages; recomputing from only surviving messages replays older ones. IDs are scoped by kind, not compared across APIs.
func newestMessageLast(messages []move, previous string) (last, newestID string) {
	previous = unmarkedLast(previous)
	var head messageHead
	if strings.HasPrefix(previous, messageLastPrefix) {
		_ = json.Unmarshal([]byte(strings.TrimPrefix(previous, messageLastPrefix)), &head)
	}
	known := make(map[string]bool, len(head.IDs))
	for _, id := range head.IDs {
		known[id] = true
	}
	for _, message := range messages {
		if message.at.IsZero() || message.at.Before(head.At) {
			continue
		}
		if message.at.After(head.At) {
			head.At, known = message.at, map[string]bool{}
		}
		if !known[message.id] {
			newestID = message.id
		}
		known[message.id] = true
	}
	if head.At.IsZero() {
		return "", ""
	}
	head.IDs = make([]string, 0, len(known))
	for id := range known {
		head.IDs = append(head.IDs, id)
	}
	sort.Strings(head.IDs)
	raw, _ := json.Marshal(head)
	return messageLastPrefix + string(raw), newestID
}

func (c *Connector) previousMessageLast(ctx context.Context, thread string) string {
	c.inbox.mu.Lock()
	epoch := c.inbox.epoch
	if previous := c.inbox.things[thread]; previous != nil {
		last := previous.Last
		c.inbox.mu.Unlock()
		return last
	}
	c.inbox.mu.Unlock()
	c.deviceState(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if record, ok := c.dev.InboxMessages[thread]; ok && lastEpoch(record.Last) == epoch {
		return record.Last
	}
	return ""
}

// Use the connector's existing sealed store so deleting a message while it restarts cannot replay an older one.
// Save before publishing the thing; a failed save leaves the notification unread for the next poll to retry.
func (c *Connector) rememberMessageLast(ctx context.Context, thread, last string, at time.Time) error {
	if !strings.HasPrefix(unmarkedLast(last), messageLastPrefix) {
		return nil
	}
	c.inbox.mu.Lock()
	same := c.inbox.things[thread] != nil && c.inbox.things[thread].Last == last
	c.inbox.mu.Unlock()
	if same {
		return nil
	}
	c.deviceState(ctx)
	c.mu.Lock()
	none, loaded := c.secrets == nil, c.devLoaded
	c.mu.Unlock()
	if none {
		return nil
	}
	if !loaded {
		return errors.New("the GitHub message cursor store could not be read")
	}
	epoch := lastEpoch(last)
	return c.saveDevice(ctx, func(d *deviceLogin) {
		if c.acct.epoch != epoch {
			return
		}
		if d.InboxMessages == nil {
			d.InboxMessages = map[string]inboxMessageRecord{}
		}
		d.InboxMessages[thread] = inboxMessageRecord{Last: last, Seen: at}
		for key, record := range d.InboxMessages {
			if lastEpoch(record.Last) != epoch || record.Seen.Before(time.Now().Add(-forgetAfter)) {
				delete(d.InboxMessages, key)
			}
		}
	})
}
