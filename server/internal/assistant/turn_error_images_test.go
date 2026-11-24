package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// #13045: accumulated pictures exceeding the model API's size, count or multi-image dimension limit are classified
// into one stable turn error code, suppress Continue, and never put main-chat readiness into setup mode.
func TestImageLimitTurnErrorCodeAndContinueSuppression(t *testing.T) {
	samples := []string{
		"Request too large (max 32MB). Accumulated images and attachments in the conversation pushed the request over the limit.",
		"API Error: 413 request_too_large: request body exceeds 32MB",
		"API Error: an image in the conversation could not be processed and was removed.",
		"API Error: 400 At least one of the image dimensions exceed max allowed size for many-image requests: 2000 pixels",
	}
	for _, text := range samples {
		if got := turnErrorCode(text); got != proto.TurnErrorImagesTooLarge {
			t.Errorf("turnErrorCode(%q) = %q, want %q", text, got, proto.TurnErrorImagesTooLarge)
		}
		if transientError(text) {
			t.Errorf("transientError(%q) = true, want false", text)
		}
		// Even a generic transient kind from a runtime must not auto-continue an image-limit failure.
		sess := apiErrorTurn("s1", text)
		sess.TurnErrorKind = proto.TurnErrorTransient
		if turnErrorTransient(sess) {
			t.Errorf("turnErrorTransient(%q) = true, want false", text)
		}
		w, by, skip := triageRules(apiErrorTurn("s1", text), time.UTC, i18n.En)
		if skip || w == nil || w.Phase != proto.WorkBlocked || w.On != proto.WaitUser || by != "rule" {
			t.Errorf("triageRules(%q) = %+v %q %v, want blocked/user by rule", text, w, by, skip)
		}
	}
	if !userTurnError(proto.TurnErrorImagesTooLarge) {
		t.Error("images_too_large must be a user turn error")
	}

	// Bare "Request too large" (from a custom gateway) is recognized by CutOff; prose mentioning it is not.
	if !assistantwire.CutOff(samples[0]) {
		t.Fatalf("CutOff did not recognize bare Request too large line")
	}
	if assistantwire.CutOff("I fixed the Request too large error by scaling the screenshots.") {
		t.Fatalf("CutOff misclassified an ordinary reply mentioning Request too large")
	}

	// Listed fleet rows stamp TurnErrorCode=images_too_large and TurnErrorKind=user.
	ft := newFleetTestNoStore(t)
	ft.d.setSessions("mac", []proto.Session{stopped("s1", samples[0]), stopped("s2", samples[2]), stopped("s3", samples[3])})
	for _, id := range []string{"s1", "s2", "s3"} {
		sess := listed(t, ft.s, "mac", id)
		if sess.TurnErrorCode != proto.TurnErrorImagesTooLarge || sess.TurnErrorKind != proto.TurnErrorUser {
			t.Fatalf("%s: TurnError=%q kind=%q code=%q", id, sess.TurnError, sess.TurnErrorKind, sess.TurnErrorCode)
		}
	}
}

func TestImageLimitReadinessDoesNotBlock(t *testing.T) {
	if readinessBlocks(ReadinessStep{State: StepProblem, Code: proto.TurnErrorImagesTooLarge}) {
		t.Fatal("images_too_large must not hold the main conversation in setup mode")
	}
	s := &Service{}
	titles := map[string]bool{}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		st := s.readinessStep(lang, StepModel, StepProblem, proto.TurnErrorImagesTooLarge, "mac", "gw", "")
		if st.Title == "" || strings.Contains(st.Title, "images_too_large") || st.Detail == "" || strings.Contains(st.Detail, "images_too_large") {
			t.Errorf("%s: missing localization: %q / %q", lang, st.Title, st.Detail)
		}
		if st.Action != nil {
			t.Errorf("%s: unexpected model-setup action %+v", lang, st.Action)
		}
		titles[st.Title] = true
	}
	if len(titles) != 3 {
		t.Errorf("expected distinct localized titles in en/zh-Hans/ja, got %v", titles)
	}
}
