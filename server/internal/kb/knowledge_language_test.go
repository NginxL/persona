package kb

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

type knowledgeReplyModel struct {
	call  func(system, user string)
	reply string
}

func (m knowledgeReplyModel) JSONMax(_ context.Context, system, user string, _ int, out any) error {
	m.call(system, user)
	return json.Unmarshal([]byte(m.reply), out)
}

// Locale is captured once per production call. A preference changed while the
// model works affects the next call, without invalidating its source snapshot.
func TestKnowledgeLanguageAtCallTimeAndProjectCommit(t *testing.T) {
	for _, changeSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "locale changes", true: "source changes"}[changeSource], func(t *testing.T) {
			e := testEngine(t, nil)
			language := i18n.En
			reads := 0
			e.OwnerLanguage = func() i18n.Lang { reads++; return language }
			original := "原名 /src/example.go — 引用のまま"
			pinned := "Do not change the owner's confirmed goal"
			p := &Project{Slug: "example", Name: original, Kind: "short", Status: "active", Dirty: true,
				Goal: pinned, Tagging: Tagging{Pinned: pinned, LockedFields: []string{"goal"}}}
			if err := e.Store.PutProject(p); err != nil {
				t.Fatal(err)
			}
			if err := e.Store.PutSummary(KindProject, p.Slug, original); err != nil {
				t.Fatal(err)
			}
			calls := 0
			e.Model = knowledgeReplyModel{reply: `{"summary":"New explanation","goal":"not authorized"}`, call: func(system, user string) {
				calls++
				if !strings.Contains(system, "in English") || !strings.Contains(user, "# Existing record") || !strings.Contains(user, original) || !strings.Contains(user, pinned) {
					t.Fatalf("wrong locale or changed source: %s\n%s", system, user)
				}
				language = i18n.Ja
				if changeSource {
					if err := e.Store.AppendLog(p.Slug, time.Now(), "owner", "New source evidence"); err != nil {
						t.Fatal(err)
					}
				}
			}}
			if n := e.RefreshProjects(context.Background(), 1); n != 1 || calls != 1 || reads != 1 {
				t.Fatalf("refresh/calls/locale reads = %d/%d/%d", n, calls, reads)
			}
			got, _ := e.Store.Project(p.Slug)
			if got.Name != original || got.Goal != pinned || got.Pinned != pinned {
				t.Fatalf("source or locked fact changed: %+v", got)
			}
			sum := e.Store.Summary(KindProject, p.Slug)
			if changeSource {
				if sum != original+"\n" {
					t.Fatalf("stale answer written: %q", sum)
				}
			} else if !strings.Contains(sum, "New explanation") {
				t.Fatalf("locale change discarded valid answer: %q", sum)
			}
		})
	}
}

func TestKnowledgeLocaleOnlyChangesNewPrompts(t *testing.T) {
	e := testEngine(t, nil)
	language := i18n.ZhHans
	e.OwnerLanguage = func() i18n.Lang { return language }
	source := "原文の名前 / 原始引用 /src/example.go"
	if err := e.Store.PutProject(&Project{Slug: "example", Name: source, Status: "active", OneLine: source}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.PutSummary(KindProject, "example", source); err != nil {
		t.Fatal(err)
	}
	putTopic(t, e, &Topic{Slug: "example", Name: source, Note: source}, topicFile{Next: 2, Points: []TopicPoint{{ID: "p1", Kind: "fact", Text: source, Path: []string{"元の分類"}, When: "2026-11-01", Sources: []TopicSource{{Kind: KindProject, ID: "example"}}}}})
	snapshot := func() map[string]string {
		t.Helper()
		files := map[string]string{}
		err := filepath.WalkDir(e.Store.Dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[path] = string(b)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	e.Model = knowledgeReplyModel{reply: `{}`, call: func(_, _ string) { t.Fatal("locale change scheduled a model call") }}
	before := snapshot()
	for _, lang := range []i18n.Lang{i18n.En, i18n.Ja, i18n.ZhHans} {
		language = lang
		prompt := e.Prompt(6, 8)
		if !strings.Contains(prompt, source) {
			t.Fatalf("source lost in %s: %s", lang, prompt)
		}
		if n := e.RefreshProjects(context.Background(), 10); n != 0 {
			t.Fatalf("locale scheduled refresh: %d", n)
		}
		if n := e.RefreshTopics(context.Background(), 10); n != 0 {
			t.Fatalf("locale scheduled topic rewrite: %d", n)
		}
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("%s changed stored files", lang)
		}
	}
}

func TestKnowledgeLanguageFallback(t *testing.T) {
	e := testEngine(t, nil)
	for _, language := range []i18n.Lang{"", "invalid", i18n.ZhHans} {
		e.OwnerLanguage = func() i18n.Lang { return language }
		if got := e.knowledgeOwner().Language; got != "" {
			t.Fatalf("%s: %q", language, got)
		}
	}
	e.OwnerLanguage = nil
	if got := e.knowledgeOwner().Language; got != "" {
		t.Fatalf("nil callback: %q", got)
	}
}

func TestKnowledgeDedupeDefiniteInEachLanguage(t *testing.T) {
	for _, tc := range []struct {
		language i18n.Lang
		reason   string
	}{
		{i18n.En, "Same release"}, {i18n.Ja, "同じ要件"}, {i18n.ZhHans, "同一个需求"},
	} {
		t.Run(string(tc.language), func(t *testing.T) {
			m := &captureModel{reply: `{"same":true,"sure":true,"keep":"B","reason":"` + tc.reason + `"}`}
			e := testEngine(t, m)
			e.OwnerLanguage = func() i18n.Lang { return tc.language }
			same, keepA, reason, err := e.judgeDuplicate(context.Background(), dedupeCandidate{a: &Project{Slug: "a", Name: "Original A"}, b: &Project{Slug: "b", Name: "Original B"}})
			if err != nil || !same || keepA || reason != tc.reason || len(m.calls) != 1 {
				t.Fatalf("same=%v keepA=%v reason=%q err=%v calls=%d", same, keepA, reason, err, len(m.calls))
			}
		})
	}
}
