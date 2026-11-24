package prompts

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Model the installed CLI's documented paging contract, not the assistant's
// behavior: chat defaults to desc, threads default to asc (lark-cli 1.0.59).
// Execute neither a shell command nor a real Lark request.
func readGroupFixturePage(t *testing.T, command string, count int) []int {
	t.Helper()
	args := strings.Fields(command)
	order, size := "desc", 50
	if slices.Contains(args, "+threads-messages-list") {
		order = "asc"
	}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--order":
			order = args[i+1]
		case "--page-size":
			var err error
			size, err = strconv.Atoi(args[i+1])
			if err != nil || size < 1 || size > 50 {
				t.Fatalf("invalid bounded page size in %q", command)
			}
		}
	}
	page := make([]int, count)
	for i := range page {
		page[i] = i + 1 // Increasing timestamps, one per message.
	}
	switch order {
	case "desc":
		slices.Reverse(page)
	case "asc":
	default:
		t.Fatalf("invalid order in %q", command)
	}
	return page[:min(size, count)]
}

func TestBotReadGroupRecentPages(t *testing.T) {
	for _, lang := range []string{"", "English", "日本語"} {
		for _, topic := range []bool{false, true} {
			for _, count := range []int{0, 20, 50, 120} {
				t.Run(fmt.Sprintf("%s/topic=%t/messages=%d", lang, topic, count), func(t *testing.T) {
					in := BotReadGroup{Language: lang, ChatID: "oc_example", MessageID: "om_example", Dir: "/tmp/read-example", Default: 40, Most: 500, FileMB: 50, TotalMB: 200, Images: 20}
					shortcut, limit := "+chat-messages-list", in.Default
					if topic {
						in.ThreadID, shortcut, limit = "omt_example", "+threads-messages-list", 50
					}
					text := RenderBotReadGroup(in)
					var command string
					for _, line := range strings.Split(text, "\n") {
						if strings.Contains(line, "lark-cli im "+shortcut+" ") {
							command = line
							break
						}
					}
					if command == "" {
						t.Fatal("rendered prompt has no listing command")
					}
					page := readGroupFixturePage(t, command, count)
					// The assistant is told to interpret the selected page chronologically.
					// Sorting cannot recover recent replies absent from the selected page.
					slices.Sort(page)
					want := make([]int, min(limit, count))
					for i := range want {
						want[i] = count - len(want) + i + 1
					}
					if !slices.Equal(page, want) {
						t.Fatalf("selected history = %v, want recent page %v; command: %s", page, want, command)
					}
				})
			}
		}
	}
}

func TestBotReadGroupRangeGuidance(t *testing.T) {
	for _, tc := range []struct {
		lang, chronological, rangeRule, partial string
	}{
		{"", "按时间从早到晚", "请求指定范围时", "读取不完整"},
		{"English", "oldest to newest", "If the request names a range", "incomplete"},
		{"日本語", "古い順", "依頼に範囲があれば", "不完全"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			text := RenderBotReadGroup(BotReadGroup{Language: tc.lang, ThreadID: "omt_example", Default: 40, Most: 500})
			for _, want := range []string{tc.chronological, tc.rangeRule, tc.partial, "--start / --end", "--page-token", "has_more", "500"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing range/history guidance %q", want)
				}
			}
			for _, unsupported := range []string{"--page-all", "--page-limit"} {
				if strings.Contains(text, unsupported) {
					t.Errorf("instructions use unsupported listing option %q", unsupported)
				}
			}
		})
	}
}
