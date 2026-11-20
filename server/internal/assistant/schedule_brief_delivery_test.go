package assistant

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestTaskBriefingModelSnapshotFailureKeepsFixedFacts(t *testing.T) {
	for _, report := range []bool{false, true} {
		name := "facts"
		if report {
			name = "report"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			cards := briefFallbackCapture(f)
			st := newSettingsFaultStore()
			s := newServiceOn(t, f, st)
			initBriefFallbackState(s)
			setSettings(t, s, func(v *Settings) { v.Main = "main" })
			task := Task{ID: "brief-job", Title: "Briefing", Prompt: "Give me the briefing", Repeat: repeatOnce,
				At: time.Now().Add(-time.Minute), MachineID: "server", Facts: !report, Report: report}
			setTasks(s, task)
			s.brief.mu.Lock()
			s.brief.byTask = map[string]*briefRun{task.ID: {facts: briefFallbackFacts(), label: "Earlier briefing"}}
			s.brief.mu.Unlock()
			st.failRead = modelRecordKey("task." + task.ID)
			if _, err := s.runTask(task, task.At, time.Now(), true, false); err == nil || f.sent() != 0 {
				t.Fatalf("model preparation failure sent a turn: %v, sends=%d", err, f.sent())
			}
			waitFor(t, "model preparation fallback notification", func() bool { return len(f.pushed()) == 1 })
			pushes := f.pushed()
			header := i18n.T(i18n.ZhHans, "assistant-briefing.fallback_header")
			if len(pushes) != 1 || pushes[0].Title != task.label() || !strings.HasPrefix(pushes[0].Body, header+"\n") || len(cards.cards()) != 1 {
				t.Fatalf("model preparation lost the fixed-facts fallback: pushes=%+v, cards=%q", pushes, cards.cards())
			}
			_, runs := scheduleState(s)
			if len(runs) != 1 || runs[0].Status != runFailed || runs[0].DeliveryUnknown {
				t.Fatalf("model preparation lost its pre-delivery failure: %+v", runs)
			}
		})
	}
}

func TestTaskBriefingUnknownDeliveryDoesNotRetryOrFallback(t *testing.T) {
	for _, report := range []bool{false, true} {
		name := "facts"
		if report {
			name = "report"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			cards := briefFallbackCapture(f)
			s := newTestService(t, f)
			initBriefFallbackState(s)
			setSettings(t, s, func(v *Settings) { v.Main = "main" })
			s.runRetry = time.Second
			f.failAssistant = rpc.ErrClosed
			synctest.Test(t, func(t *testing.T) {
				task := Task{ID: "brief-job", Title: "Briefing", Prompt: "Give me the briefing", Repeat: repeatOnce,
					At: time.Now().Add(-time.Minute), MachineID: "server", Facts: !report, Report: report}
				setTasks(s, task)
				if _, err := s.runTask(task, task.At, time.Now(), false, false); err == nil {
					t.Fatal("lost turn receipt reported success")
				}
				<-time.After(10 * s.runRetry)
				synctest.Wait()
				_, runs := scheduleState(s)
				if len(runs) != 1 || !runs[0].DeliveryUnknown || runs[0].Status != runFailed {
					t.Fatalf("unknown briefing delivery was retried: %+v", runs)
				}
				if len(f.pushed()) != 1 || len(cards.cards()) != 0 {
					t.Fatalf("unknown delivery needs one failure notice, no fallback: pushes=%+v, cards=%q", f.pushed(), cards.cards())
				}
			})
		})
	}
}
