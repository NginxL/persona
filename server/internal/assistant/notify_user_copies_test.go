package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
)

// runToolOn is runTool for conversation self on machine: the copies' marks and the mirrors are keyed by both.
func runToolOn(s *Service, machine, self, name string, args any) (string, error) {
	raw, _ := json.Marshal(args)
	r := s.Tool(context.Background(), ToolCall{Name: name, Arguments: raw, MachineID: machine, SessionID: self})
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	if r.IsError {
		return "", errors.New(b.String())
	}
	return b.String(), nil
}

// notifyCopyService is a scheduled task's conversation s1 with the Feishu bot's pushes on and two extension channels
// with pushes on, and a hub that answers notify.request with res.
func notifyCopyService(t *testing.T, res NotifyResult) (*Service, *fakeHub) {
	t.Helper()
	s, f := channelPushService(t, false)
	enableChannelPushFeishu(f)
	f.notifyResult = res
	return s, f
}

// A notice the hub's low-distraction mode held for the briefing (a P1 one, or a repeat of a related key within 12
// hours) gets no copy on the Feishu bot or the channels either, and marks none of them pushed: the scheduled task's
// turn-end mirror then goes as it would without the notice (#8268).
func TestNotifyUserHeldByPushGradingSendsNoChannelCopy(t *testing.T) {
	for _, reason := range []string{"ln_p1_held", "ln_dedup_12h"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				zero := 0
				s, f := notifyCopyService(t, NotifyResult{OK: true, Held: true, Reason: reason, Devices: &zero})
				out, err := runToolOn(s, "server", "s1", "notify_user", map[string]any{"title": "Status", "body": "The notification", "related_key": "pr-1"})
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if want := i18n.T(s.lang(t.Context()), "assistant-notify.ln_held_notice"); out != want {
					t.Fatalf("answer = %q, want the held answer %q with no word of a copy", out, want)
				}
				if got := f.appParams(AppFeishu, FeishuPost); len(got) != 0 {
					t.Fatalf("the Feishu bot got %v for a held notice", got)
				}
				if got := f.appParams(AppChannels, ChannelsSend); len(got) != 0 {
					t.Fatalf("the channels got %v for a held notice", got)
				}
				for _, ch := range []string{channel.Feishu, "examplechat", "otherchat"} {
					if s.pushedLately(ch, "server", "s1") {
						t.Fatalf("%s marked pushed for a held notice: its mirror would stand aside for a copy it never got", ch)
					}
				}
				// The turn ends: each channel's mirror delivers the task's answer as usual.
				s.extensionMirror(context.Background(), "server", "s1")
				s.feishuMirror(context.Background(), "server", "s1")
				synctest.Wait()
				if got := f.appParams(AppChannels, ChannelsSend); len(got) != 2 {
					t.Fatalf("channel mirrors = %v, want the turn's answer on both channels", got)
				}
				if got := f.appParams(AppFeishu, FeishuPost); len(got) == 0 {
					t.Fatal("the Feishu mirror did not deliver the turn")
				}
			})
		})
	}
}

// A notice the hub let through (P0, the main conversation's ordinary one, or any with low-distraction mode off) is
// copied as before; the answer says where the copies went, and with no device taking the push it no longer says the
// user was not alerted. The copies mark their channels pushed, so the turn-end mirrors stand aside.
func TestNotifyUserLetThroughIsCopiedAndSaysSo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		zero := 0
		s, f := notifyCopyService(t, NotifyResult{OK: true, Devices: &zero, Reason: "no_device"})
		out, err := runToolOn(s, "server", "s1", "notify_user", map[string]any{"title": "Status", "body": "The notification", "push_level": "P0"})
		if err != nil {
			t.Fatal(err)
		}
		l := s.lang(t.Context())
		want := i18n.T(l, "assistant-notify.no_device_copied")
		for _, name := range []string{i18n.T(l, "assistant-channels_runs.feishu"), "examplechat", "otherchat"} {
			want = i18n.T(l, "assistant-notify.then", i18n.Args{"first": want, "second": i18n.T(l, "assistant-notify.copy_sent", i18n.Args{"name": name})})
		}
		if out != want {
			t.Fatalf("answer = %q, want %q", out, want)
		}
		if out == i18n.T(l, "assistant-notify.no_device_notice") {
			t.Fatal("the answer still says the user was not alerted")
		}
		synctest.Wait()
		if got := f.appParams(AppFeishu, FeishuPost); len(got) != 1 {
			t.Fatalf("Feishu posts = %v, want the one copy", got)
		}
		if got := f.appParams(AppChannels, ChannelsSend); len(got) != 2 {
			t.Fatalf("channel sends = %v, want one copy per channel", got)
		}
		s.extensionMirror(context.Background(), "server", "s1")
		synctest.Wait()
		if got := f.appParams(AppChannels, ChannelsSend); len(got) != 2 {
			t.Fatalf("channel sends after the turn = %v, want no mirror next to the copies", got)
		}
	})
}

// The Feishu copy's first card has not landed when notify_user's wait is over: the answer says the copy was sent and
// its delivery is not confirmed; the card still lands afterwards and marks the bot pushed.
func TestNotifyUserCopyNotConfirmedInTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, entered, release := feishuPushHub(t, nil)
		landed := sync.OnceFunc(func() { close(release) })
		defer landed() // a failure below must not leave the card's post hanging in the bubble
		zero := 0
		s.hub.(*fakeHub).notifyResult = NotifyResult{OK: true, Devices: &zero, Reason: "no_device"}
		l := s.lang(t.Context())
		start := time.Now()
		out, err := runToolOn(s, "server", "main-conv", "notify_user", map[string]any{"title": "Reminder", "body": "Details"})
		if err != nil {
			t.Fatal(err)
		}
		<-entered
		if waited := time.Since(start); waited != notifyCopyWait {
			t.Fatalf("waited %v for the copy, want %v", waited, notifyCopyWait)
		}
		want := i18n.T(l, "assistant-notify.then", i18n.Args{"first": i18n.T(l, "assistant-notify.no_device_copied"),
			"second": i18n.T(l, "assistant-notify.copy_pending", i18n.Args{"name": i18n.T(l, "assistant-channels_runs.feishu")})})
		if out != want {
			t.Fatalf("answer = %q, want %q", out, want)
		}
		landed()
		synctest.Wait()
		if !s.pushedLately(channel.Feishu, "server", "main-conv") {
			t.Fatal("the late card did not mark the bot pushed")
		}
	})
}

// A Feishu copy that fails, with no device taking the push either: the answer says both, and that the user was not
// alerted.
func TestNotifyUserCopyFailedSaysTheUserWasNotAlerted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, release := feishuPushHub(t, errors.New("feishu down"))
		close(release)
		zero := 0
		s.hub.(*fakeHub).notifyResult = NotifyResult{OK: true, Devices: &zero, Reason: "no_device"}
		out, err := runToolOn(s, "server", "main-conv", "notify_user", map[string]any{"title": "Reminder", "body": "Details"})
		if err != nil {
			t.Fatal(err)
		}
		l := s.lang(t.Context())
		want := i18n.T(l, "assistant-notify.then", i18n.Args{"first": i18n.T(l, "assistant-notify.no_device_copied"),
			"second": i18n.T(l, "assistant-notify.copy_failed", i18n.Args{"name": i18n.T(l, "assistant-channels_runs.feishu")})})
		want = i18n.T(l, "assistant-notify.then", i18n.Args{"first": want, "second": i18n.T(l, "assistant-notify.not_alerted")})
		if out != want {
			t.Fatalf("answer = %q, want %q", out, want)
		}
	})
}

// With the bot's pushes off and no channel, nothing is copied: the answer is the devices' alone, as before.
func TestNotifyUserWithoutCopiesAnswersAsBefore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.ownModels = true
		f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
			return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), Features: FeishuFeatures{Main: true}}, nil
		})
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) { return map[string]any{"channels": []channel.View{}}, nil })
		zero := 0
		f.notifyResult = NotifyResult{OK: true, Devices: &zero, Reason: "no_device"}
		s := newTestService(t, f)
		start := time.Now()
		out, err := runToolOn(s, "server", "main-conv", "notify_user", map[string]any{"title": "Reminder", "body": "Details"})
		if err != nil {
			t.Fatal(err)
		}
		if want := i18n.T(s.lang(t.Context()), "assistant-notify.no_device_notice"); out != want {
			t.Fatalf("answer = %q, want %q", out, want)
		}
		if waited := time.Since(start); waited != 0 {
			t.Fatalf("waited %v with nothing to copy", waited)
		}
		if got := f.appParams(AppFeishu, FeishuPost); len(got) != 0 {
			t.Fatalf("Feishu posts = %v with its pushes off", got)
		}
	})
}

// What the answer says for each devices outcome once copies went out, in English and Chinese.
func TestNotifyUserAnswerWithCopies(t *testing.T) {
	devices := func(n int) *int { return &n }
	feishu := func(state string) pushCopy { return pushCopy{channel: channel.Feishu, state: state} }
	cases := []struct {
		lang   i18n.Lang
		res    NotifyResult
		copies []pushCopy
		want   string
	}{
		{"en", NotifyResult{OK: true, Devices: devices(0), Reason: "no_device"}, []pushCopy{feishu(copyPending)},
			"No phone, computer or browser took the push (0 devices). Copied to Feishu (delivery not confirmed)."},
		{"en", NotifyResult{OK: true, Devices: devices(0), Reason: "no_device"}, []pushCopy{feishu(copySent)},
			"No phone, computer or browser took the push (0 devices). Copied to Feishu: delivered."},
		{"en", NotifyResult{OK: true, Devices: devices(2)}, []pushCopy{feishu(copySent), {channel: "examplechat", name: "Example Chat", state: copyFailed}},
			"Notification sent to 2 devices. Copied to Feishu: delivered. The copy to Example Chat failed to send."},
		{"en", NotifyResult{OK: true, Devices: devices(0), Reason: "filtered"}, []pushCopy{feishu(copyFailed)},
			"No device took the push (0 devices): every device that could has these notifications turned off. The copy to Feishu failed to send. The user was not alerted. Do not say the user was notified."},
		{"en", NotifyResult{OK: true}, []pushCopy{feishu(copySent)}, "Notification sent. Copied to Feishu: delivered."},
		{"en", NotifyResult{OK: true, Queued: true}, []pushCopy{feishu(copySent)},
			"The hub is unreachable now: the push to the devices is kept and goes out once it is back. Copied to Feishu: delivered."},
		{"zh-Hans", NotifyResult{OK: true, Devices: devices(0), Reason: "no_device"}, []pushCopy{feishu(copyPending)},
			"推送没有设备收到（设备 0 台：现在没有手机、电脑或浏览器能收这个 hub 的通知）。已抄送飞书（送达未确认）。"}, // i18n-ignore: the zh-Hans answer under test
		{"zh-Hans", NotifyResult{OK: true, Devices: devices(0), Kept: 1, Reason: "no_device"}, []pushCopy{feishu(copySent)},
			"推送还没有设备收到（现在设备 0 台）：它在等 1 台离线设备，30 分钟内连上会显示。已抄送飞书，已送到。"}, // i18n-ignore: the zh-Hans answer under test
	}
	for _, c := range cases {
		if got := notifyAnswerText(c.lang, c.res, c.copies); got != c.want {
			t.Errorf("%s %+v %+v:\n got %q\nwant %q", c.lang, c.res, c.copies, got, c.want)
		}
	}
}
