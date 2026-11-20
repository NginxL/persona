package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// run starts the interactive Claude Code and serves the node until stdin ends, the node stops it, or Claude Code
// exits. Messages and control requests that come while it starts wait for it (control requests are answered at once);
// a start that fails hands them to claude -p (fallback).
func (b *tuiBridge) run(ctx context.Context, lines <-chan []byte, stdout io.Writer) int {
	b.tr = newTUITranslator()
	ready := make(chan error, 1)
	go func() {
		err := b.launch()
		if err == nil {
			err = b.awaitReady(ctx)
		}
		if err == nil {
			err = b.openTranscript()
		}
		ready <- err
	}()
	var pending [][]byte
	for started := false; !started; {
		select {
		case err := <-ready:
			if err != nil {
				b.shutdown()
				if ctx.Err() != nil {
					return 0
				}
				return b.fallback(ctx, err.Error(), pending, lines, stdout)
			}
			started = true
		case l, ok := <-lines:
			if !ok {
				b.shutdown()
				return 0
			}
			if b.control(ctx, l, false) {
				continue
			}
			pending = append(pending, l)
		case <-ctx.Done():
			<-ready
			b.shutdown()
			return 0
		}
	}
	defer b.shutdown()
	b.out.emit(map[string]any{"type": "system", "subtype": tuiReadySubtype})
	inputs := make(chan []byte, 256)
	go b.submitLoop(ctx, inputs)
	for _, l := range pending {
		inputs <- l
	}
	exited := make(chan int, 1)
	go func() { exited <- b.watch(ctx) }()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return 0 // the node closed stdin: it lets the process go
			}
			if !b.control(ctx, l, true) {
				inputs <- l
			}
		case status := <-exited:
			return status
		case <-ctx.Done():
			return 0
		}
	}
}

// control answers a control request line (true), or says the line is a message (false). Before Claude Code is ready
// (started false) nothing can be pressed in it.
func (b *tuiBridge) control(ctx context.Context, l []byte, started bool) bool {
	var c tuiControl
	if json.Unmarshal(l, &c) != nil || c.Type != "control_request" {
		return false
	}
	subtype, _ := c.Request["subtype"].(string)
	var err error
	switch subtype {
	case "initialize":
	case "interrupt": // the stream-json request; the apps' Stop ends the process, as for claude -p (assistantInterrupt)
		if now, _ := c.Request["send_now"].(bool); now {
			err = errors.New("send now is not available while the assistant runs in interactive Claude Code")
		} else if !started {
			err = errors.New("interactive Claude Code has not started yet")
		} else {
			err = b.tm.Keys(ctx, tuiPane, "escape")
		}
	case "background_tasks":
		if !started {
			err = errors.New("interactive Claude Code has not started yet")
		} else {
			err = b.tm.Keys(ctx, tuiPane, "ctrl+b") // as in the terminal: what runs in the foreground goes to the background
		}
	case "set_permission_mode", "apply_flag_settings":
		err = errors.New("interactive Claude Code takes this when it starts: the conversation restarts with it")
	default:
		err = fmt.Errorf("%s is not available while the assistant runs in interactive Claude Code", subtype)
	}
	b.out.answer(c.RequestID, err)
	return true
}

// tuiUserLine is a user message the node writes: its content is a list of blocks (assistant.send) or a plain string
// (the /compact of a compaction, assistant_compact.go and assistant_reap.go).
type tuiUserLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// tuiUserContent reads a line the node writes as a user message's content blocks; false for anything else, which is
// said on stderr rather than dropped without a word.
func tuiUserContent(l []byte) ([]map[string]any, bool) {
	var u tuiUserLine
	if json.Unmarshal(l, &u) != nil || u.Type != "user" {
		return nil, false
	}
	var blocks []map[string]any
	if json.Unmarshal(u.Message.Content, &blocks) == nil {
		return blocks, true
	}
	var s string
	if json.Unmarshal(u.Message.Content, &s) == nil {
		return []map[string]any{{"type": "text", "text": s}}, true
	}
	return nil, false
}

// submitLoop types the messages into Claude Code one at a time, in order. A message it cannot type fails its turn
// (undelivered), and so do the ones already waiting behind it: typed after it, they would be answered as if it had
// been said.
func (b *tuiBridge) submitLoop(ctx context.Context, inputs <-chan []byte) {
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-inputs:
			content, ok := tuiUserContent(l)
			if !ok {
				fmt.Fprintf(b.stderr, "assistant-tui: a line that is no user message was not typed: %.200s\n", l)
				continue
			}
			n++
			text, err := tuiMessageText(content, b.dir, n)
			if err == nil {
				err = b.typeIn(ctx, text)
			}
			if err != nil && ctx.Err() == nil {
				b.undelivered(err, 1+dropWaiting(inputs))
			}
		}
	}
}

// dropWaiting empties inputs of the messages already waiting and counts them.
func dropWaiting(inputs <-chan []byte) int {
	count := 0
	for {
		select {
		case l := <-inputs:
			if _, ok := tuiUserContent(l); ok {
				count++
			}
		default:
			return count
		}
	}
}

// typeIn types a message's text into Claude Code (submit; typeText in tests).
func (b *tuiBridge) typeIn(ctx context.Context, text string) error {
	if b.typeText != nil {
		return b.typeText(ctx, text)
	}
	return b.submit(ctx, text)
}

// undelivered tells the node that count messages never reached Claude Code, and why.
func (b *tuiBridge) undelivered(err error, count int) {
	fmt.Fprintf(b.stderr, "assistant-tui: %d message(s) not submitted: %v\n", count, err)
	note := "Interactive Claude Code did not get your message (" + err.Error() + "). Send it again."
	if count > 1 {
		note = fmt.Sprintf("Interactive Claude Code did not get your last %d messages (%v). Send them again.", count, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ev := range b.tr.undeliverable(note) {
		b.out.emit(ev)
	}
}

// tuiTypeableWait bounds the wait for the input box to take a message (a dialog on screen, a permission question being
// answered on the phone).
var tuiTypeableWait = 10 * time.Minute

// submit pastes text into the input box and presses Enter, until the box is empty again. A draft left in the box
// is a message of the bridge's that missed its Enter: Enter sends it first.
func (b *tuiBridge) submit(ctx context.Context, text string) error {
	deadline := time.Now().Add(tuiTypeableWait)
	draftSince := time.Time{}
	for {
		_, ansi, err := b.screens(ctx)
		if err != nil {
			return err
		}
		code, _ := screenCheck(ansi)
		if code == "" {
			break
		}
		if code == "input_draft" {
			if draftSince.IsZero() {
				draftSince = time.Now()
			} else if time.Since(draftSince) > 3*time.Second {
				_ = b.tm.Keys(ctx, tuiPane, "enter")
				draftSince = time.Time{}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the input box did not take a message within %s (%s)", tuiTypeableWait, code)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err := b.tm.SendText(ctx, tuiPane, text); err != nil {
		return err
	}
	if !b.waitDraft(ctx, true, 5*time.Second) {
		return errors.New("the pasted message did not show in the input box")
	}
	for range 3 {
		if err := b.tm.Keys(ctx, tuiPane, "enter"); err != nil {
			return err
		}
		if b.waitDraft(ctx, false, 3*time.Second) {
			return nil
		}
	}
	return errors.New("the message stayed in the input box after Enter")
}

// waitDraft waits up to d for the input box to hold a draft (want) or not.
func (b *tuiBridge) waitDraft(ctx context.Context, want bool, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(150 * time.Millisecond) {
		if _, ansi, err := b.screens(ctx); err == nil && screenDraft(ansi) == want {
			return true
		}
	}
	return false
}

// tuiMessageText is a message's content as what is typed: its texts, and each image saved in dir with its path, as a
// path pasted into the terminal is. A first character Claude Code's input box reads as a mode switch (! runs a shell
// command, # was memory) gets a space before it, so the words go to the model as they did through claude -p.
func tuiMessageText(content []map[string]any, dir string, n int) (string, error) {
	var parts []string
	for i, c := range content {
		switch c["type"] {
		case "text":
			if t, _ := c["text"].(string); t != "" {
				parts = append(parts, t)
			}
		case "image":
			path, err := saveTUIImage(c, dir, n, i)
			if err != nil {
				return "", err
			}
			parts = append(parts, "[Image attached: "+path+"]")
		}
	}
	text := strings.Join(parts, "\n\n")
	if strings.HasPrefix(text, "!") || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "&") {
		text = " " + text
	}
	return text, nil
}
