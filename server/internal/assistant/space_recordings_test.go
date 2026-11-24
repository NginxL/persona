package assistant

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// A recording saved into a track (#13501) is a Markdown file: its front matter names the recording and the track's
// categories, then one line per sentence with its time from the start; the name sorts by the day it started.
func TestRenderSpaceRecording(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	start := time.Date(2026, 11, 22, 1, 30, 0, 0, time.UTC)
	end := start.Add(3725 * time.Second)
	u := kb.RecordingUpload{Revision: 3, StartedAt: start, EndedAt: &end, Duration: 3725.4, Language: "en-US", Device: "phone",
		Title: "Example \"budget\" review", Kind: kb.RecordingMeeting, Complete: true,
		Segments: []kb.RecordingSegment{{Start: 0, End: 2, Text: "Hello  there."}, {Start: 3723, End: 3725, Text: "Bye.", Echo: true}}}
	got := renderSpaceRecording("0b6f1c9e-3a52-4d1e-9a8f-2c4b5d6e7f80", u, []string{"finance"}, loc)
	want := "---\n" +
		"recording: 0b6f1c9e-3a52-4d1e-9a8f-2c4b5d6e7f80\n" +
		"title: \"Example \\\"budget\\\" review\"\n" +
		"kind: meeting\n" +
		"started_at: 2026-11-22T09:30:00+08:00\n" +
		"ended_at: 2026-11-22T10:32:05+08:00\n" +
		"duration_seconds: 3725\n" +
		"language: \"en-US\"\n" +
		"device: phone\n" +
		"complete: true\n" +
		"revision: 3\n" +
		"categories: [\"finance\"]\n" +
		"---\n\n" +
		"# Example \"budget\" review\n\n" +
		"[0:00] Hello there.\n" +
		"[1:02:03] (Persona) Bye.\n"
	if got != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}

	plain := renderSpaceRecording("0b6f1c9e-3a52-4d1e-9a8f-2c4b5d6e7f80", kb.RecordingUpload{Revision: 1, StartedAt: start, Device: "watch", Kind: kb.RecordingNote}, nil, loc)
	wantPlain := "---\nrecording: 0b6f1c9e-3a52-4d1e-9a8f-2c4b5d6e7f80\nkind: note\nstarted_at: 2026-11-22T09:30:00+08:00\ndevice: watch\n" +
		"complete: false\nrevision: 1\ncategories: []\n---\n\n# 2026-11-22 09:30\n\n"
	if plain != wantPlain {
		t.Errorf("untitled:\n%s\nwant:\n%s", plain, wantPlain)
	}

	late := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC) // the next day in UTC+8
	if name := spaceRecordingName("0b6f1c9e-3a52-4d1e-9a8f-2c4b5d6e7f80", late, loc); name != "2026-11-23-0b6f1c9e-3a52-4d1e-9a8f-2c4b5d6e7f80.md" {
		t.Errorf("name = %q", name)
	}
}
