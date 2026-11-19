package hub

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
)

type trustedSubmissionKey struct{}
type clarificationRequestProofKey struct{}
type verifiedClarificationKey struct{}

func withClarificationRequestProof(r *http.Request, token string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), clarificationRequestProofKey{}, token))
}

func clarificationRequestProof(ctx context.Context) string {
	token, _ := ctx.Value(clarificationRequestProofKey{}).(string)
	return token
}

func withVerifiedClarification(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, verifiedClarificationKey{}, devicewire.Submission{
		ID: rand.Text(), Kind: devicewire.SubmissionApprovalClarification, OriginalText: text,
	})
}

// captureHumanSubmission runs before to-do, project or model-context augmentation.
// A cached principal or a caller-supplied provenance descriptor is not evidence.
// The brief DB admission is released here, never held across session startup.
func (h *Hub) captureHumanSubmission(r *http.Request, text string) *http.Request {
	p, ok := r.Context().Value(principalKey{}).(principal)
	if !ok || p.ID == "" || p.Role != roleFull || h.store == nil || h.isMember() {
		return r
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return r
	}
	hash := hashToken(strings.TrimPrefix(auth, "Bearer "))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	token, err := h.store.DeviceTokenByHash(ctx, hash)
	if err != nil || token == nil || token.ID != p.ID || token.Role != roleFull || token.Kind == "link" {
		return r
	}
	caller := devicewire.Caller{Kind: "device", Issuer: h.hubID(), CredentialID: token.ID, AdmissionID: rand.Text()}
	if s := h.ownerAuthStore(); s != nil {
		if binding, release, err := s.AdmitOwner(ctx, h.hubID(), hash); err == nil {
			caller.OwnerSubject = *binding.Subject
			caller.BindingRevision = binding.Revision
			release()
		}
	}
	out := devicewire.WithCaller(r.Context(), caller)
	out = context.WithValue(out, trustedSubmissionKey{}, devicewire.Submission{ID: rand.Text(), Kind: "human_input", OriginalText: text})
	return r.WithContext(out)
}

// captureOwnerAnswer reuses full-device admission, then carries only the answer provenance across the bus.
func (h *Hub) captureOwnerAnswer(r *http.Request) *http.Request {
	admitted := h.captureHumanSubmission(r, "")
	if _, ok := admitted.Context().Value(trustedSubmissionKey{}).(devicewire.Submission); ok {
		return r.WithContext(devicewire.WithOwnerAnswer(r.Context()))
	}
	return r
}

// attachNodeCaller excludes manager-originated device.call, regardless of any
// principal retained on a tool's transport. Reserved fields live in the envelope.
func attachNodeCaller(ctx context.Context, p *devicewire.NodeCall) {
	caller := devicewire.Caller{Kind: "system"}
	if _, manager := callerOf(ctx); !manager {
		if c := devicewire.CallerOf(ctx); c.Kind == "device" {
			caller = c
		}
	}
	p.Caller = &caller
	if caller.Kind == "device" {
		if submission, ok := ctx.Value(trustedSubmissionKey{}).(devicewire.Submission); ok {
			p.Submission = &submission
		}
	}
	if submission, ok := ctx.Value(verifiedClarificationKey{}).(devicewire.Submission); ok {
		// A manager's call reaches here only after the hub consumed the exact
		// one-time proof. The manager itself can never attach this Submission.
		p.Submission = &submission
	}
}
