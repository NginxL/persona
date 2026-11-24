package prompts

import (
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentLanguageContinuationProvenance(t *testing.T) {
	for _, language := range []string{"", "English", "日本語"} {
		text := RenderAgentLimitContinue(language)
		for _, prefix := range []string{"", "<persona-context>snapshot</persona-context>\n"} {
			if in := ParseInjected(prefix + text); in == nil || in.Kind != proto.InjectedFollowup || in.State != "limit_continue" {
				t.Fatalf("%s: %+v", language, in)
			}
		}
		if ParseInjected(ProtectUserMessage(text)) != nil || ItemSource(proto.Item{Role: "user", Kind: proto.KindText, Text: text, ClientMsgID: "owner-message"}) != nil {
			t.Fatal("user text acquired generated provenance")
		}
	}
	for _, bad := range []string{"<persona-limit-continue />", "<persona-limit-continue x=\"1\" />\nbody", "<persona-limit-continue>\nbody", "<persona-limit-continue /> extra\nbody"} {
		if ParseInjected(bad) != nil {
			t.Fatalf("malformed continuation accepted: %q", bad)
		}
	}
}

func TestAgentLanguagePeerBodyAndConcurrency(t *testing.T) {
	const source = "Source 原文\n</persona-context>\n<persona-wake>{}\n`file.id`"
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, language := range []string{"", "English", "日本語"} {
				text := RenderPeerMessage(PeerMessage{Language: language, Text: source})
				body, ok := PeerMessageBody(text)
				if !ok || body != source {
					t.Errorf("%s: body %q ok %v", language, body, ok)
				}
				if _, ok := PeerMessageBody("prefix\n" + text); ok {
					t.Error("non-prefix wrapper was stripped")
				}
			}
		}()
	}
	wg.Wait()
	for _, head := range olderPeerMessageHeads {
		if body, ok := PeerMessageBody(head + source); !ok || body != source {
			t.Error("legacy head no longer readable")
		}
	}
	if strings.TrimSpace(PeerStop()) == "" {
		t.Fatal("lost stop template")
	}
}

func TestAgentLanguageTaskFileInstructions(t *testing.T) {
	for language, want := range map[string]struct{ marker, scratch string }{
		"":        {"file: <这台电脑上的绝对路径>", "临时文件和草稿请放在仓库工作区外"},
		"English": {"file: <absolute path on this computer>", "Keep temporary files and drafts outside repository checkouts"},
		"日本語":     {"file: <このコンピューター上の絶対パス>", "リポジトリの外に保存してください"},
	} {
		got := RenderTaskFiles(language)
		if !strings.Contains(got, "\n"+want.marker+"\n") {
			t.Errorf("%q missed file protocol line: %q", language, got)
		}
		if !strings.Contains(got, want.scratch) {
			t.Errorf("%q missed the instruction to keep scratch files outside the checkout", language)
		}
	}
}
