package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

// A batch's last acknowledgement belongs to another event, not to the new request and screenshot.
// Stub verdicts check prompt scope and per-event persistence, not the model's semantic accuracy.
func TestInboxGroupTiersEachEventByItsOwnActivity(t *testing.T) {
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		t.Run(locale, func(t *testing.T) {
			batch := groupBatch()
			batch.Group = &connector.ThingGroup{Batch: "chat:F1@1062", New: []string{"m1060", "m1061", "m1062"}}
			batch.Last = "1062"
			batch.Context = "Example peer（@了我）: @Example owner the filter returns no results; please check it (m1060)\n" +
				"Example peer: [Image: img_example_filter] (m1061)\n" +
				"Another peer: The deployment is fixed now, thanks (m1062)"
			g := &groupLark{batch: batch, placed: map[string]bool{}}
			model, client := newGroupModel(t, `{
				"assign":[
					{"message":"m1060","event":"lark:F1:1051"},
					{"message":"m1061","event":"lark:F1:1051"},
					{"message":"m1062","event":"lark:F1:900"}],
				"events":[
					{"event":"lark:F1:1051","title":"Filter returns no results","summary":"A new failure needs review","tier":"action","reason":"New request needs review"},
					{"event":"lark:F1:900","title":"Deployment confirmed fixed","summary":"Current issue explicitly resolved","tier":"fyi","reason":"Resolution confirmed","reopen":false}]}`)
			h, st := groupHub(t, g, client)
			h.f.hello.Locale = locale
			request, resolved := h.inbox.items["lark:F1:1051"], h.inbox.items["lark:F1:900"]
			request.Title, request.Summary, request.Tier = "Earlier filter report", "The earlier report was acknowledged", TierFYI
			resolved.Title, resolved.Summary = "Deployment issue", "Deployment was fixed"
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			call := onlyCall(t, model.asked())
			for _, want := range []string{
				"分级（tier）看动到之后这件事整体的情况",
				"分别判断每个事项的整体状态",
				"归入该事项的新活动及其仍未解决的请求",
				"不能用其他事项的回复或确认替它结案",
			} {
				if !strings.Contains(call.System, want) {
					t.Errorf("the grouping criteria lost per-event scope %q", want)
				}
			}
			if strings.Contains(call.System, "分拣的是本次新消息") {
				t.Error("grouping was told to tier one new message rather than each event")
			}
			for _, want := range []string{"lark:F1:1051", "lark:F1:900", "m1060", "m1061", "m1062", "img_example_filter"} {
				if !strings.Contains(call.User, want) {
					t.Errorf("the mixed-event input lost %q", want)
				}
			}
			if request.Tier != TierAction || request.DoneAt != nil || request.TriagedBy != "model" || request.Last != "1061" {
				t.Errorf("the new request did not keep its own verdict: %+v", request)
			}
			if resolved.Tier != TierFYI || resolved.DoneAt == nil || resolved.Last != "1062" {
				t.Errorf("the other event did not stay resolved: %+v", resolved)
			}
			if len(g.appends) != 2 || !slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) ||
				!slices.Equal(g.appends[1].Messages, []string{"m1062"}) {
				t.Errorf("messages were not written to their own events: %+v", g.appends)
			}
			if !st.saved(request.ID) || !st.saved(resolved.ID) {
				t.Error("the independent event verdicts were not saved")
			}
		})
	}
}
