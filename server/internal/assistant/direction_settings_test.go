package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

// directionSettingCall is one persona_settings call whose card may keep it waiting.
type directionSettingCall struct {
	text  string
	isErr bool
}

func callDirectionSetting(s *Service, session, args string) directionSettingCall {
	r := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", SessionID: session,
		Arguments: json.RawMessage(args)})
	return directionSettingCall{text: r.Content[0].Text, isErr: r.IsError}
}

func setDirectionSetting(s *Service, session, value string) chan directionSettingCall {
	done := make(chan directionSettingCall, 1)
	go func() {
		done <- callDirectionSetting(s, session, `{"action":"set","key":"direction.conv_lead.prompt","value":`+value+`}`)
	}()
	return done
}

func answerDirectionSettingCard(t *testing.T, s *Service, id string, allow bool) {
	t.Helper()
	raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: id, Answer: raw}); err != nil {
		t.Fatal(err)
	}
}

func waitDirectionSetting(t *testing.T, done <-chan directionSettingCall) directionSettingCall {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("persona_settings did not return")
		return directionSettingCall{}
	}
}

func directionSettingNotes(s *Service) string {
	return s.directionOf("conv_lead").Notes
}

// Direction notes are discoverable and readable from any assistant conversation, but only the main conversation may
// propose a change. A refused side conversation raises no card and cannot change the stored notes.
func TestDirectionPromptSettingReadAndMainOnly(t *testing.T) {
	s, _ := notesEnv(t)
	setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
	const before = "# Rules\n\nVerify on a real phone."
	if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", before, nil); err != nil {
		t.Fatal(err)
	}
	if got := callDirectionSetting(s, "side_chat", `{"action":"get","key":"direction.conv_lead.prompt"}`); got.isErr || got.text != before {
		t.Fatalf("side-chat get = %+v, want the complete notes", got)
	}
	if got := callDirectionSetting(s, "side_chat", `{"action":"list"}`); got.isErr ||
		!strings.Contains(got.text, "direction.conv_lead.prompt") || !strings.Contains(got.text, "Weather") {
		t.Fatalf("list does not discover the named direction setting: %+v", got)
	}
	if got := callDirectionSetting(s, "side_chat", `{"action":"get","key":"direction.nope.prompt"}`); !got.isErr || !strings.Contains(got.text, "no such direction") {
		t.Fatalf("unknown-direction get = %+v, want an explicit refusal", got)
	}
	if got := callDirectionSetting(s, "side_chat", `{"action":"set","key":"direction.conv_lead.prompt","value":"overwritten"}`); !got.isErr ||
		!strings.Contains(got.text, "only the main conversation") {
		t.Fatalf("side-chat set = %+v, want a main-only refusal", got)
	}
	if pending := s.pendingQuestions(); len(pending) != 0 {
		t.Fatalf("a refused side-chat set raised a card: %+v", pending)
	}
	if got := directionSettingNotes(s); got != before {
		t.Fatalf("refused set changed notes to %q", got)
	}
}

func TestDirectionPromptEditRejectsNullAndOverlappingFragments(t *testing.T) {
	for _, tc := range []struct {
		name, before, value string
	}{
		{name: "null new", before: "alpha", value: `{"old":"alpha","new":null}`},
		{name: "overlapping old", before: "ababa", value: `{"old":"aba","new":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if after, err := directionPromptEdit(tc.before, json.RawMessage(tc.value)); err == nil {
				t.Fatalf("edit = %q, nil error; want rejection before any card", after)
			}
		})
	}
}

// Fragment edits are exact and unambiguous. Invalid edits fail before a card or write, rather than silently replacing
// the whole prompt or guessing which occurrence the owner meant.
func TestDirectionPromptSettingFragmentValidation(t *testing.T) {
	s, _ := notesEnv(t)
	setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
	const before = "alpha\n\nrepeat\n\nrepeat"
	if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", before, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty old", value: `{"old":"","new":"x"}`, want: "old must not be empty"},
		{name: "missing old", value: `{"old":"missing","new":"x"}`, want: "was not found"},
		{name: "ambiguous old", value: `{"old":"repeat","new":"x"}`, want: "more than once"},
		{name: "missing new", value: `{"old":"alpha"}`, want: "old and new"},
		{name: "unknown field", value: `{"old":"alpha","new":"x","extra":true}`, want: "old and new"},
		{name: "null", value: `null`, want: "whole-prompt string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callDirectionSetting(s, "main_chat", `{"action":"set","key":"direction.conv_lead.prompt","value":`+tc.value+`}`); !got.isErr ||
				!strings.Contains(got.text, tc.want) {
				t.Fatalf("set = %+v, want an error containing %q", got, tc.want)
			}
			if pending := s.pendingQuestions(); len(pending) != 0 {
				t.Fatalf("invalid edit raised a card: %+v", pending)
			}
			if got := directionSettingNotes(s); got != before {
				t.Fatalf("invalid edit changed notes to %q", got)
			}
		})
	}
}

// A late edit must be visible on the card, not clipped away by the unchanged prefix. Deny it after inspection so this
// check also leaves the stored notes untouched.
func TestDirectionPromptSettingCardShowsChangedLateFragment(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
	}{
		{name: "ASCII", prefix: strings.Repeat("Long unchanged context. ", 20)},
		{name: "CJK", prefix: strings.Repeat("前", 140)},
		{name: "emoji", prefix: strings.Repeat("🙂", 140)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := notesEnv(t)
			setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
			before := tc.prefix + "\n\nOld final rule."
			if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", before, nil); err != nil {
				t.Fatal(err)
			}
			done := setDirectionSetting(s, "main_chat", `{"old":"Old final rule.","new":"New final rule."}`)
			card, ok := awaitCard(s)
			if !ok {
				t.Fatal("no late-fragment confirmation card")
			}
			answerDirectionSettingCard(t, s, card.ID, false)
			if got := waitDirectionSetting(t, done); got.isErr {
				t.Fatalf("denied late-fragment edit = %+v", got)
			}
			for _, want := range []string{"Old final rule.", "New final rule."} {
				if !strings.Contains(card.Summary, want) {
					t.Errorf("confirmation card %q hides the changed fragment %q", card.Summary, want)
				}
			}
			if got := directionSettingNotes(s); got != before {
				t.Fatalf("denial changed notes to %q", got)
			}
		})
	}
}

// A fragment may be deleted without replacing the rest of the prompt. An edit that leaves the text unchanged returns
// immediately without asking the owner to approve a no-op.
func TestDirectionPromptSettingFragmentDeletionAndNoOp(t *testing.T) {
	s, _ := notesEnv(t)
	setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
	const before = "alpha\n\nremove me\n\nomega"
	if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", before, nil); err != nil {
		t.Fatal(err)
	}
	if got := callDirectionSetting(s, "main_chat", `{"action":"set","key":"direction.conv_lead.prompt","value":{"old":"remove me","new":"remove me"}}`); got.isErr ||
		!strings.Contains(got.text, "unchanged") {
		t.Fatalf("no-op set = %+v", got)
	}
	if pending := s.pendingQuestions(); len(pending) != 0 {
		t.Fatalf("no-op set raised a card: %+v", pending)
	}

	done := setDirectionSetting(s, "main_chat", `{"old":"\n\nremove me","new":""}`)
	card, ok := awaitCard(s)
	if !ok {
		t.Fatal("no fragment-deletion confirmation card")
	}
	answerDirectionSettingCard(t, s, card.ID, true)
	if got := waitDirectionSetting(t, done); got.isErr {
		t.Fatalf("approved fragment deletion = %+v", got)
	}
	if got := directionSettingNotes(s); got != "alpha\n\nomega" {
		t.Fatalf("fragment deletion = %q", got)
	}
}

// The real manager chain: read the notes, propose a one-paragraph replacement from the main conversation, inspect the
// owner's before → after card, and approve it through the normal interaction action. Only then do the saved notes, the
// lead's role policy and the next direction Agent's first-prompt context change. A whole-string set then replaces all.
func TestDirectionPromptSettingOwnerApprovalChain(t *testing.T) {
	s, _ := notesEnv(t)
	setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
	const before = "# Rules\n\nVerify before merge.\n\nNo Friday deploys."
	if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", before, nil); err != nil {
		t.Fatal(err)
	}
	if got := callDirectionSetting(s, "main_chat", `{"action":"get","key":"direction.conv_lead.prompt"}`); got.isErr || got.text != before {
		t.Fatalf("initial get = %+v", got)
	}

	done := setDirectionSetting(s, "main_chat", `{"old":"Verify before merge.","new":"Run server tests before merge."}`)
	card, ok := awaitCard(s)
	if !ok {
		t.Fatal("no owner confirmation card")
	}
	for _, want := range []string{"Weather", "Verify before merge.", "Run server tests before merge."} {
		if !strings.Contains(card.Summary, want) {
			t.Errorf("confirmation card %q does not contain %q", card.Summary, want)
		}
	}
	if got := directionSettingNotes(s); got != before {
		t.Fatalf("notes changed before approval to %q", got)
	}
	answerDirectionSettingCard(t, s, card.ID, true)
	if got := waitDirectionSetting(t, done); got.isErr || !strings.Contains(got.text, "direction.conv_lead.prompt") {
		t.Fatalf("approved partial set = %+v", got)
	}
	const partial = "# Rules\n\nRun server tests before merge.\n\nNo Friday deploys."
	if got := directionSettingNotes(s); got != partial {
		t.Fatalf("approved partial notes = %q, want %q", got, partial)
	}
	if got := callDirectionSetting(s, "side_chat", `{"action":"get","key":"direction.conv_lead.prompt"}`); got.isErr || got.text != partial {
		t.Fatalf("post-approval get = %+v", got)
	}
	if policy := dispatchedPolicy(t, s, "conv_lead"); !strings.Contains(policy, "Run server tests before merge.") ||
		strings.Contains(policy, "Verify before merge.") {
		t.Fatalf("lead policy did not take the approved notes: %q", policy)
	}
	if prompt := s.withProjectContext(context.Background(), "box", "/code/weather/ios", "", "Fix the login page"); !strings.Contains(prompt, "Run server tests before merge.") || strings.Contains(prompt, "Verify before merge.") {
		t.Fatalf("new Agent prompt did not take the approved notes: %q", prompt)
	}

	done = setDirectionSetting(s, "main_chat", `"# Rules\n\nWhole replacement."`)
	card, ok = awaitCard(s)
	if !ok {
		t.Fatal("no whole-replacement confirmation card")
	}
	answerDirectionSettingCard(t, s, card.ID, true)
	if got := waitDirectionSetting(t, done); got.isErr {
		t.Fatalf("approved whole set = %+v", got)
	}
	if got := directionSettingNotes(s); got != "# Rules\n\nWhole replacement." {
		t.Fatalf("whole replacement = %q", got)
	}
}

// Current Leaders membership, notes_at and the write are serialized with direction end. The direction clock fires
// under the direction lock after the membership check but before the write. If ending the direction finishes there,
// approval must be stale; if the approved write holds the settings change lock, ending waits and may succeed after it.
func TestDirectionPromptSettingDirectionEndDuringApprovalIsSerialized(t *testing.T) {
	s, _ := notesEnv(t)
	setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
	if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", "before", nil); err != nil {
		t.Fatal(err)
	}
	change, err := s.setDirectionPromptSetting(context.Background(), "conv_lead", "Weather", json.RawMessage(`"after"`))
	if err != nil {
		t.Fatal(err)
	}
	b := &s.directions
	oldNow := b.now
	t.Cleanup(func() { b.now = oldNow })
	reachedWrite := make(chan struct{})
	finishWrite := make(chan struct{})
	b.now = func() time.Time {
		close(reachedWrite)
		<-finishWrite
		return time.Now()
	}
	applyDone := make(chan error, 1)
	go func() {
		_, err := change.apply(context.Background())
		applyDone <- err
	}()
	<-reachedWrite
	endDone := make(chan error, 1)
	go func() {
		_, err := s.updateSettings(context.Background(), func(v *Settings) error {
			v.Leaders = []string{"conv_other"}
			return nil
		})
		endDone <- err
	}()
	endedBeforeWrite := false
	var endErr error
	select {
	case endErr = <-endDone:
		endedBeforeWrite = true
	case <-time.After(50 * time.Millisecond):
	}
	close(finishWrite)
	applyErr := <-applyDone
	if !endedBeforeWrite {
		endErr = <-endDone
	}
	if endErr != nil {
		t.Fatal(endErr)
	}
	if endedBeforeWrite {
		if !errors.Is(applyErr, approvalops.ErrStale) {
			t.Fatalf("direction ended before the write, approval = %v, want stale", applyErr)
		}
		if got := directionSettingNotes(s); got != "before" {
			t.Fatalf("ended direction's notes changed to %q", got)
		}
		return
	}
	if applyErr != nil {
		t.Fatalf("approval serialized before direction end = %v", applyErr)
	}
	if got := directionSettingNotes(s); got != "after" {
		t.Fatalf("approved notes = %q, want after", got)
	}
}

// Denial and a concurrent edit both leave the owner's newer state alone. In particular, approval does not rebase a
// stale proposal merely because its clipped before/after card text still looks the same.
func TestDirectionPromptSettingDenialAndStaleCardChangeNothing(t *testing.T) {
	t.Run("denied", func(t *testing.T) {
		s, _ := notesEnv(t)
		setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
		if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", "before", nil); err != nil {
			t.Fatal(err)
		}
		done := setDirectionSetting(s, "main_chat", `"after"`)
		card, ok := awaitCard(s)
		if !ok {
			t.Fatal("no owner confirmation card")
		}
		answerDirectionSettingCard(t, s, card.ID, false)
		if got := waitDirectionSetting(t, done); got.isErr || !strings.Contains(got.text, "declined") {
			t.Fatalf("denied set = %+v", got)
		}
		if got := directionSettingNotes(s); got != "before" {
			t.Fatalf("denial changed notes to %q", got)
		}
	})

	t.Run("stale", func(t *testing.T) {
		s, _ := notesEnv(t)
		setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
		if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", "before", nil); err != nil {
			t.Fatal(err)
		}
		done := setDirectionSetting(s, "main_chat", `"approved proposal"`)
		card, ok := awaitCard(s)
		if !ok {
			t.Fatal("no owner confirmation card")
		}
		if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", "newer app edit", nil); err != nil {
			t.Fatal(err)
		}
		answerDirectionSettingCard(t, s, card.ID, true)
		if got := waitDirectionSetting(t, done); got.isErr || !strings.Contains(got.text, "fresh approval") {
			t.Fatalf("stale approval = %+v, want a fresh-approval outcome", got)
		}
		if got := directionSettingNotes(s); got != "newer app edit" {
			t.Fatalf("stale approval overwrote the newer notes: %q", got)
		}
	})
}
