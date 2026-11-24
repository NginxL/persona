package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Two independent loopback IMAP servers let one account fail without affecting the other.
func crossAccountService(t *testing.T, goodB bool) *Service {
	t.Helper()
	a, b := newTestIMAP(t), newTestIMAP(t)
	s := newServiceWithAccount(t, a)
	a.appendMessage(t, "INBOX", rawMessage("Quarterly A", "a@example.com", "synthetic A"))
	a.appendMessage(t, "INBOX", rawMessage("Other A", "a@example.com", "synthetic A"))
	newer := strings.Replace(rawMessage("Quarterly B", "b@example.com", "synthetic B"),
		"Mon, 02 Jan 2006 15:04:05 -0700", "Tue, 03 Jan 2006 15:04:05 -0700", 1)
	b.appendMessage(t, "INBOX", newer)
	account := b.testAccount()
	account.ID = "acc_b"
	if !goodB {
		account.Password = "synthetic-wrong"
	}
	s.mu.Lock()
	s.accts[account.ID] = account
	s.mu.Unlock()
	return s
}

func setCrossAccountPassword(s *Service, id, password string) {
	s.mu.Lock()
	s.accts[id].Password = password
	s.mu.Unlock()
	s.dropConn(id)
}

func TestCrossAccountSearchCompleteness(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"query":"quarterly"}`)
	t.Run("complete and paging", func(t *testing.T) {
		s := crossAccountService(t, true)
		all, err := s.ListMessages(ctx, ListOptions{Query: "quarterly"})
		if err != nil || all == nil || len(all.Messages) != 2 || all.Messages[0].AccountID != "acc_b" || all.Messages[1].AccountID != "acc_test" {
			t.Fatalf("merged results = %+v, %v", all, err)
		}
		page, err := s.ListMessages(ctx, ListOptions{Query: "quarterly", Limit: 1})
		if err != nil || len(page.Messages) != 1 || page.Messages[0].AccountID != "acc_b" || !page.HasMore || page.NextBeforeUID != 0 {
			t.Fatalf("merged page = %+v, %v", page, err)
		}
		one, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Limit: 1})
		if err != nil || len(one.Messages) != 1 || !one.HasMore || one.NextBeforeUID == 0 {
			t.Fatalf("single-account page = %+v, %v", one, err)
		}
		older, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Limit: 1, BeforeUID: one.NextBeforeUID})
		if err != nil || len(older.Messages) != 1 || older.Messages[0].Subject != "Quarterly A" {
			t.Fatalf("single-account continuation = %+v, %v", older, err)
		}
		code, body := doJSON(t, s, http.MethodGet, "/api/v1/mail/messages?q=quarterly", "")
		var rows []MailSummary
		if code != http.StatusOK || json.Unmarshal(body["messages"], &rows) != nil || len(rows) != 2 {
			t.Fatalf("HTTP complete = %d %s", code, body["messages"])
		}
		text := s.Call(ctx, ToolSearch, args, nil)
		if text.IsError || !strings.Contains(resultText(t, text), "Quarterly A") || !strings.Contains(resultText(t, text), "Quarterly B") {
			t.Fatalf("MCP text complete = %+v", text)
		}
		raw, ok, err := s.CallJSON(ctx, ToolSearch, args)
		var out searchJSON
		if !ok || err != nil || json.Unmarshal(raw, &out) != nil || len(out.Messages) != 2 {
			t.Fatalf("MCP JSON complete = %s, %v, %v", raw, ok, err)
		}
	})
	t.Run("mixed failure never becomes success", func(t *testing.T) {
		s := crossAccountService(t, false)
		one, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test", Query: "quarterly"})
		if err != nil || len(one.Messages) != 1 {
			t.Fatalf("healthy explicit account = %+v, %v", one, err)
		}
		res, err := s.ListMessages(ctx, ListOptions{Query: "quarterly"})
		var incomplete *IncompleteSearchError
		if res != nil || !errors.As(err, &incomplete) {
			t.Errorf("incomplete service result = %+v, %v", res, err)
		}
		if res, err := s.ListMessages(ctx, ListOptions{Query: "no-match-anywhere"}); err == nil || res != nil {
			t.Errorf("empty healthy account hid failure = %+v, %v", res, err)
		}
		if res, err := s.ListMessages(ctx, ListOptions{}); err == nil || res != nil {
			t.Errorf("incomplete unified inbox = %+v, %v", res, err)
		}
		code, body := doJSON(t, s, http.MethodGet, "/api/v1/mail/messages?q=quarterly", "")
		if code < 400 || len(body["messages"]) != 0 {
			t.Errorf("incomplete HTTP = %d %+v", code, body)
		}
		text := s.Call(ctx, ToolSearch, args, nil)
		if !text.IsError || strings.Contains(resultText(t, text), "Quarterly A") {
			t.Errorf("incomplete MCP text = %+v", text)
		}
		raw, ok, err := s.CallJSON(ctx, ToolSearch, args)
		if !ok || err == nil || len(raw) != 0 {
			t.Errorf("incomplete MCP JSON = %s, %v, %v", raw, ok, err)
		}
		setCrossAccountPassword(s, "acc_b", "secret")
		repaired, err := s.ListMessages(ctx, ListOptions{Query: "quarterly"})
		if err != nil || len(repaired.Messages) != 2 {
			t.Fatalf("repaired aggregate = %+v, %v", repaired, err)
		}
		if res := s.Call(ctx, ToolSearch, args, nil); res.IsError || !strings.Contains(resultText(t, res), "Quarterly B") {
			t.Errorf("repaired MCP text = %+v", res)
		}
		code, body = doJSON(t, s, http.MethodGet, "/api/v1/mail/messages?q=quarterly", "")
		var rows []MailSummary
		if code != http.StatusOK || json.Unmarshal(body["messages"], &rows) != nil || len(rows) != 2 {
			t.Errorf("repaired HTTP = %d %s", code, body["messages"])
		}
		raw, ok, err = s.CallJSON(ctx, ToolSearch, args)
		var jsonOut searchJSON
		if !ok || err != nil || json.Unmarshal(raw, &jsonOut) != nil || len(jsonOut.Messages) != 2 {
			t.Errorf("repaired MCP JSON = %s, %v, %v", raw, ok, err)
		}
	})
	t.Run("healthy empty and all failed", func(t *testing.T) {
		s := crossAccountService(t, true)
		empty, err := s.ListMessages(ctx, ListOptions{Query: "no-match-anywhere"})
		if err != nil || empty == nil || len(empty.Messages) != 0 {
			t.Fatalf("healthy empty = %+v, %v", empty, err)
		}
		if res := s.Call(ctx, ToolSearch, json.RawMessage(`{"query":"no-match-anywhere"}`), nil); res.IsError || !strings.Contains(resultText(t, res), "No messages matched") {
			t.Errorf("healthy empty text = %+v", res)
		}
		code, body := doJSON(t, s, http.MethodGet, "/api/v1/mail/messages?q=no-match-anywhere", "")
		var rows []MailSummary
		if code != http.StatusOK || json.Unmarshal(body["messages"], &rows) != nil || len(rows) != 0 {
			t.Errorf("healthy empty HTTP = %d %s", code, body["messages"])
		}
		raw, ok, err := s.CallJSON(ctx, ToolSearch, json.RawMessage(`{"query":"no-match-anywhere"}`))
		var jsonOut searchJSON
		if !ok || err != nil || json.Unmarshal(raw, &jsonOut) != nil || len(jsonOut.Messages) != 0 {
			t.Errorf("healthy empty JSON = %s, %v, %v", raw, ok, err)
		}
		setCrossAccountPassword(s, "acc_test", "synthetic-wrong")
		setCrossAccountPassword(s, "acc_b", "synthetic-wrong")
		if res, err := s.ListMessages(ctx, ListOptions{Query: "quarterly"}); err == nil || res != nil {
			t.Errorf("all failed service = %+v, %v", res, err)
		}
		code, _ = doJSON(t, s, http.MethodGet, "/api/v1/mail/messages?q=quarterly", "")
		if code < 400 {
			t.Errorf("all failed HTTP = %d", code)
		}
		if res := s.Call(ctx, ToolSearch, args, nil); !res.IsError {
			t.Errorf("all failed MCP = %+v", res)
		}
		if raw, ok, err := s.CallJSON(ctx, ToolSearch, args); !ok || err == nil || len(raw) != 0 {
			t.Errorf("all failed JSON = %s, %v, %v", raw, ok, err)
		}
	})
}

func TestCrossAccountCancellationAfterOneAccountFinishes(t *testing.T) {
	s := crossAccountService(t, true)
	blocked := s.accountLock("acc_b")
	if err := blocked.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer blocked.release()
	started := make(chan struct{})
	s.imapDial = func(a *MailAccount) (*imapclient.Client, error) {
		if a.ID == "acc_test" {
			close(started)
		}
		return dialAndLogin(a, "", nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type answer struct {
		result *ListResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := s.ListMessages(ctx, ListOptions{Query: "quarterly"})
		done <- answer{result, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy account did not start")
	}
	// The healthy goroutine owns this lock until its result is complete.
	wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	finished := s.accountLock("acc_test")
	if err := finished.acquire(wait); err != nil {
		t.Fatal(err)
	}
	finished.release()
	cancel()
	select {
	case got := <-done:
		if got.result != nil || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("canceled aggregate = %+v, %v", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled aggregate did not return")
	}
}

func TestCrossAccountCanceledCallers(t *testing.T) {
	s := crossAccountService(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args := json.RawMessage(`{"query":"quarterly"}`)
	if result, err := s.ListMessages(ctx, ListOptions{Query: "quarterly"}); result != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled service = %+v, %v", result, err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages?q=quarterly", nil).WithContext(ctx)
	s.Handler().ServeHTTP(w, r)
	if w.Code < 400 {
		t.Errorf("canceled HTTP = %d %s", w.Code, w.Body)
	}
	if res := s.Call(ctx, ToolSearch, args, nil); !res.IsError {
		t.Errorf("canceled MCP = %+v", res)
	}
	if raw, ok, err := s.CallJSON(ctx, ToolSearch, args); !ok || !errors.Is(err, context.Canceled) || len(raw) != 0 {
		t.Errorf("canceled JSON = %s, %v, %v", raw, ok, err)
	}
}

func TestCrossAccountErrorPrivacyAndLocalization(t *testing.T) {
	s := crossAccountService(t, false)
	s.imapDial = func(a *MailAccount) (*imapclient.Client, error) {
		if a.ID == "acc_b" {
			return nil, fmt.Errorf("synthetic-wrong credential for private@example.com")
		}
		return dialAndLogin(a, "", nil)
	}
	for _, c := range []struct {
		lang i18n.Lang
		want string
	}{
		{i18n.En, "Couldn't load mail from every account. Check the accounts and try again."},
		{i18n.ZhHans, "未能读取所有邮箱账号的邮件。请检查账号后重试。"},
		{i18n.Ja, "すべてのメールアカウントからメールを読み込めませんでした。アカウントを確認してもう一度お試しください。"},
	} {
		t.Run(string(c.lang), func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(mailLanguageWriter{w, c.lang}, httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages?q=quarterly", nil))
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadGateway || body.Code != "mail_search_incomplete" || body.Error != c.want || strings.Contains(w.Body.String(), "synthetic-wrong") || strings.Contains(w.Body.String(), "private@example.com") {
				t.Fatalf("unsafe or untyped HTTP failure: %d %s", w.Code, w.Body)
			}
			ctx := i18n.With(context.Background(), c.lang)
			res := s.Call(ctx, ToolSearch, json.RawMessage(`{"query":"quarterly"}`), nil)
			if !res.IsError || len(res.Content) != 1 || res.Content[0].Text != body.Error {
				t.Fatalf("localized MCP = %+v; HTTP = %s", res, body.Error)
			}
		})
	}
}
