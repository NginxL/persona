package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A task from the main conversation whose directory does not exist on its computer: the agent never starts. The main
// conversation, the only one that can retry it, hears of it at once, even when a supervision message just went to
// another conversation (the background supervisor) and the shared gap would hold it for minutes. The task, stored done
// with its start error (#1077), is not shown as finished: the to-do list files it as waiting for action, and the
// assistant's snapshot lists it first as a failed start to handle.
func TestFailedStartWakesTheMainConversationAtOnce(t *testing.T) {
	s, f := newQuietService(t)
	setSettings(t, s, func(v *Settings) { v.MainTasksQuiet = new(false) }) // not quiet: the incident's main-conversation task
	s.taskRetry = 20 * time.Millisecond
	// Every other signal waits an hour after the last message; one just went out.
	s.superviseTimes = superviseTimings{gather: 20 * time.Millisecond, every: time.Hour, urgent: time.Hour}
	s.supervise.mu.Lock()
	s.supervise.last = time.Now()
	s.supervise.mu.Unlock()

	todo := reviewTestTask(t, s, f, map[string]any{"cwd": "/Users/nobody"},
		errors.New("session.start: directory /Users/nobody does not exist"))

	// (a) the failed-start wake, not held back by the gap
	waitFor(t, "the failed-start supervision message in the main conversation", func() bool {
		for _, n := range f.notices() {
			if strings.Contains(n, "📋 督办") && strings.Contains(n, "没开起来") && strings.Contains(n, todo.ID) {
				return true
			}
		}
		return false
	})

	// (b) not presented as an ordinary finished task
	got := todoNow(s, todo.ID)
	if got.StartError == "" || !strings.Contains(got.StartError, "does not exist") {
		t.Fatalf("todo = %+v, want its start error kept", got)
	}
	if tr := triageTodo(listedTodo{Todo: got}, noSessions, time.Now(), time.Time{}, time.UTC, false); tr.Section != triageWaiting || tr.Rule != ruleFailedStart {
		t.Fatalf("triage = %+v, want waiting / failed_start", tr)
	}
	if line := todoLine(got, time.Now(), time.UTC); !strings.Contains(line, "[failed to start") || strings.Contains(line, "[done") {
		t.Fatalf("todo line = %q", line)
	}
	tasks := s.snapshotTasks(context.Background(), "")
	if len(tasks) == 0 || tasks[0].ID != todo.ID {
		t.Fatalf("snapshot tasks = %+v, want the failed start first", tasks)
	}
	if tasks[0].State != "没开起来" || !strings.Contains(tasks[0].Health, "启动失败，待你处理") {
		t.Fatalf("snapshot task = %+v", tasks[0])
	}
}

// Only a failed start skips the gap; every other signal still waits it out.
func TestDigestWaitFailedStartIsImmediate(t *testing.T) {
	now := time.Date(2026, 11, 13, 8, 51, 0, 0, time.UTC)
	p := superviseTimings{gather: 15 * time.Second, every: 10 * time.Minute, urgent: 2 * time.Minute}
	last := now.Add(-36 * time.Second) // 08:50:24, the background supervisor's message
	if got, ok := digestWait(now, last, []superviseSignal{{Kind: sigFailed, At: now}}, p); !ok || got != p.gather {
		t.Fatalf("failed start waits %v, %v; want %v", got, ok, p.gather)
	}
	if got, _ := digestWait(now, last, []superviseSignal{{Kind: sigNeedsYou, At: now}}, p); got != p.urgent-36*time.Second {
		t.Fatalf("needs-you waits %v; want the urgent gap", got)
	}
}
