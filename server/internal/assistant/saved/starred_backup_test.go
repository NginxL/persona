package saved

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func backupMessage(number int) starBackupMessage {
	return starBackupMessage{
		Key:          fmt.Sprintf("sm%032x", number),
		Conversation: "agent",
		Role:         "assistant",
		Title:        "Architecture review",
		Speaker:      "Reviewer",
		Text:         "A saved decision, with a newline.\n**Keep this part.**",
		StarredAt:    time.Date(2026, time.November, 20, 15, 45, 0, 123_000_000, time.UTC),
	}
}

func backupJSON(t *testing.T, messages []starBackupMessage, revision *int64) string {
	t.Helper()
	body := map[string]any{"format": starBackupFormat, "version": starBackupVersion, "messages": messages}
	if revision != nil {
		body["revision"] = *revision
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestStarBackupPreviewAndImportPreserveArchivalSnapshot(t *testing.T) {
	store := &starTestStore{}
	book, mux, events := newStarTestBook(store)
	var capability struct {
		CanImport bool `json:"can_import"`
	}
	doStar(t, mux, "GET", "/import", "full", "", 200, &capability)
	if !capability.CanImport || len(*events) != 0 {
		t.Fatalf("unexpected import capability: %+v, %v", capability, *events)
	}

	local, err := book.add(t.Context(), testStar("existing"))
	if err != nil {
		t.Fatal(err)
	}
	message := backupMessage(1)
	message.StarredAt = local.StarredAt.Add(-time.Hour)
	message.Text = "较早的决定 🐈\n\n**保留这条记录。**"
	written := message.StarredAt.Add(-time.Hour)
	message.WrittenAt = &written
	message.Omitted = 17
	duplicate := backupMessage(2)
	duplicate.Key = local.ID
	duplicate.Text = "Different words with the same key must not overwrite the original."
	messages := []starBackupMessage{message, duplicate}

	var preview starBackupPreview
	doStar(t, mux, "POST", "/import/preview", "full", backupJSON(t, messages, nil), 200, &preview)
	wantPreview := starBackupPreview{Revision: 1, Total: 2, ToAdd: 1, AlreadySaved: 1, Available: StarredMax - 1}
	if preview != wantPreview || len(*events) != 1 {
		t.Fatalf("preview mutated storage or returned wrong counts: %+v, %v", preview, *events)
	}
	doc, err := loadStars(t.Context(), store)
	if err != nil || doc.Revision != 1 || len(doc.Stars) != 1 || !reflect.DeepEqual(doc.Stars[0], local) {
		t.Fatalf("preview changed snapshots: %+v, %v", doc, err)
	}

	var posted starBackupImport
	doStar(t, mux, "POST", "/import", "full", backupJSON(t, messages, &preview.Revision), 200, &posted)
	if posted != (starBackupImport{Revision: 2, Added: 1, AlreadySaved: 1}) || len(*events) != 2 {
		t.Fatalf("restore result or invalidation: %+v, %v", posted, *events)
	}
	doc, err = loadStars(t.Context(), store)
	if err != nil || doc.Revision != 2 || len(doc.Stars) != 2 {
		t.Fatalf("restore did not commit the batch: %+v, %v", doc, err)
	}
	original, archive := doc.Stars[0], doc.Stars[1]
	if !reflect.DeepEqual(original, local) || !reflect.DeepEqual(archive.StarInput, message.snapshot()) ||
		!archive.StarredAt.Equal(message.StarredAt) || !validStarBackupKey(archive.ID) || archive.ID == message.Key {
		t.Fatalf("restore changed originals, display data or identity: %+v, %+v", original, archive)
	}
	if archive.Machine != backupMachine || archive.Session != backupSession+message.Key || !proto.PositionalItemID(archive.Item) ||
		archive.SourceFingerprint != "" || archive.SourceManager != "" {
		t.Fatalf("archive impersonates a live source: %+v", archive.StarSource)
	}
	live := testStar(backupItem)
	live.SourceFingerprint = strings.Repeat("a", 32)
	if sameStarSource(archive, live) {
		t.Fatal("archival key marked a live positional transcript row")
	}

	// A different manager instance reads the same versioned collection.
	_, fresh, _ := newStarTestBook(store)
	doStar(t, fresh, "POST", "/import/preview", "full", backupJSON(t, messages, nil), 200, &preview)
	if preview != (starBackupPreview{Revision: 2, Total: 2, AlreadySaved: 2, Available: StarredMax - 2}) {
		t.Fatalf("backup key was not retained across reload: %+v", preview)
	}
	doStar(t, fresh, "POST", "/import", "full", backupJSON(t, messages, &preview.Revision), 200, &posted)
	if posted != (starBackupImport{Revision: 2, AlreadySaved: 2}) {
		t.Fatalf("repeat restore should be a no-op: %+v", posted)
	}
	doc, err = loadStars(t.Context(), store)
	if err != nil || len(doc.Stars) != 2 || doc.Revision != 2 {
		t.Fatalf("repeat restore wrote again: %+v, %v", doc, err)
	}
}

func TestStarBackupRejectsInvalidInputAndUnauthorizedRoles(t *testing.T) {
	store := &starTestStore{}
	_, mux, events := newStarTestBook(store)
	empty := []starBackupMessage{}
	zero := int64(0)
	for _, route := range []struct{ method, path, body string }{
		{"GET", "/import", ""},
		{"POST", "/import/preview", backupJSON(t, empty, nil)},
		{"POST", "/import", backupJSON(t, empty, &zero)},
	} {
		doStar(t, mux, route.method, route.path, "read", route.body, 403, nil)
	}

	message := backupMessage(3)
	valid := backupJSON(t, []starBackupMessage{message}, nil)
	for _, body := range []string{
		`{"format":"other","version":1,"messages":[]}`,
		`{"format":"persona.starred","version":2,"messages":[]}`,
		`{"format":"persona.starred","version":1,"messages":null}`,
		`{"format":"persona.starred","format":"persona.starred","version":1,"messages":[]}`,
		`{"format":"persona.starred","version":1,"messages":[],"revision":0}`,
		`{"format":"persona.starred","version":1,"messages":[]}{}`,
		backupJSON(t, []starBackupMessage{message, message}, nil),
		strings.Replace(valid, `"speaker":`, `"machine_id":"spoof","speaker":`, 1),
		strings.Replace(valid, `"speaker":`, `"source_fingerprint":"spoof","speaker":`, 1),
		strings.Replace(valid, `"speaker":`, `"source_manager":"spoof","speaker":`, 1),
		strings.Replace(valid, `"speaker":`, `"title":"duplicate","speaker":`, 1),
		strings.Replace(valid, `"speaker":"Reviewer",`, "", 1),
		strings.Replace(valid, `"speaker":"Reviewer"`, `"speaker":null`, 1),
		strings.Replace(valid, `"speaker":"Reviewer"`, `"speaker":234`, 1),
		strings.Replace(valid, "Reviewer", string([]byte{0xff}), 1), // invalid UTF-8 must not be replaced and accepted
		strings.Replace(valid, `"role":"assistant"`, `"role":"tool"`, 1),
		strings.Replace(valid, `"conversation":"agent"`, `"conversation":"unknown"`, 1),
		strings.Replace(valid, `"starred_at":"2026-11-20T15:45:00.123Z"`, `"starred_at":"unparseable"`, 1),
		strings.Replace(valid, `"starred_at":`, `"written_at":null,"starred_at":`, 1),
		strings.Replace(valid, `"starred_at":`, `"omitted_chars":-1,"starred_at":`, 1),
	} {
		doStar(t, mux, "POST", "/import/preview", "full", body, 400, nil)
	}
	badKey := message
	badKey.Key = "sm" + strings.Repeat("A", 32)
	badText := message
	badText.Key = backupMessage(4).Key
	badText.Text = strings.Repeat("🐈", StarTextMax+1)
	badTitle := message
	badTitle.Key = backupMessage(5).Key
	badTitle.Title = strings.Repeat("案", starLabelMax+1)
	badShortLabel := message
	badShortLabel.Key, badShortLabel.Speaker = backupMessage(6).Key, "Reviewer "
	badShortPrefix := message
	badShortPrefix.Key, badShortPrefix.Text, badShortPrefix.Omitted = backupMessage(7).Key, strings.Repeat(" ", StarTextMax-1), 1
	badNoOmissions := message
	badNoOmissions.Key, badNoOmissions.Text = backupMessage(8).Key, strings.Repeat(" ", StarTextMax)
	for _, invalid := range []starBackupMessage{badKey, badText, badTitle, badShortLabel, badShortPrefix, badNoOmissions} {
		doStar(t, mux, "POST", "/import", "full", backupJSON(t, []starBackupMessage{message, invalid}, &zero), 400, nil)
	}
	doStar(t, mux, "POST", "/import", "full", valid, 400, nil)
	doStar(t, mux, "POST", "/import", "full", `{"format":"persona.starred","version":1,"messages":[],"revision":null}`, 400, nil)
	doStar(t, mux, "POST", "/import/preview", "full", strings.Repeat(" ", assistantwire.StarredImportBodyMax+1), 413, nil)

	var posted starBackupImport
	doStar(t, mux, "POST", "/import", "full", backupJSON(t, empty, &zero), 200, &posted)
	if store.json != nil || len(*events) != 0 || posted != (starBackupImport{}) {
		t.Fatalf("denied, invalid, or empty imports changed storage: %+v, %+v, %v", store, posted, *events)
	}
}

func TestStarBackupKeepsLegacyZeroWrittenAt(t *testing.T) {
	_, original, _ := newStarTestBook(&starTestStore{})
	input := testStar("legacy-zero-timestamp")
	input.WrittenAt = new(time.Time)
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var posted struct{ Star Star }
	doStar(t, original, "POST", "", "full", string(raw), 200, &posted)
	if posted.Star.WrittenAt == nil || !posted.Star.WrittenAt.IsZero() {
		t.Fatalf("ordinary snapshots should preserve the old timestamp: %+v", posted.Star)
	}

	message := starBackupMessage{
		Key: posted.Star.ID, Conversation: posted.Star.Conversation, Role: posted.Star.Role,
		Title: posted.Star.Title, Speaker: posted.Star.Speaker, Text: posted.Star.Text,
		StarredAt: posted.Star.StarredAt, WrittenAt: posted.Star.WrittenAt,
	}
	destination := &starTestStore{}
	_, restored, _ := newStarTestBook(destination)
	messages := []starBackupMessage{message}
	var preview starBackupPreview
	doStar(t, restored, "POST", "/import/preview", "full", backupJSON(t, messages, nil), 200, &preview)
	doStar(t, restored, "POST", "/import", "full", backupJSON(t, messages, &preview.Revision), 200, nil)
	doc, err := loadStars(t.Context(), destination)
	if err != nil || len(doc.Stars) != 1 || doc.Stars[0].WrittenAt == nil || !doc.Stars[0].WrittenAt.IsZero() {
		t.Fatalf("restore changed the original timestamp: %+v, %v", doc, err)
	}
}

func TestStarBackupKeepsLegacyClippedLabelsAndWhitespacePrefix(t *testing.T) {
	_, original, _ := newStarTestBook(&starTestStore{})
	input := testStar("legacy-clipped-snapshot")
	title, speaker := strings.Repeat("t", starLabelMax-1)+" ", strings.Repeat("話", starLabelMax-1)+" "
	prefix := strings.Repeat("\u202f", StarTextMax)
	input.Title, input.Speaker, input.Text = title+"source", speaker+"author", prefix+"visible"
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var posted struct{ Star Star }
	doStar(t, original, "POST", "", "full", string(raw), 200, &posted)
	saved := posted.Star
	if saved.Title != title || saved.Speaker != speaker || saved.Text != prefix || saved.Omitted != len("visible") {
		t.Fatalf("legacy source did not produce the expected clipped snapshot: %q, %q, %d", saved.Title, saved.Speaker, saved.Omitted)
	}
	message := starBackupMessage{
		Key: saved.ID, Conversation: saved.Conversation, Role: saved.Role,
		Title: saved.Title, Speaker: saved.Speaker, Text: saved.Text, StarredAt: saved.StarredAt, Omitted: saved.Omitted,
	}
	var revision int64
	destination := &starTestStore{}
	_, restored, _ := newStarTestBook(destination)
	doStar(t, restored, "POST", "/import", "full", backupJSON(t, []starBackupMessage{message}, &revision), 200, nil)
	doc, err := loadStars(t.Context(), destination)
	if err != nil || len(doc.Stars) != 1 || !reflect.DeepEqual(doc.Stars[0].StarInput, message.snapshot()) {
		t.Fatalf("restore did not preserve the snapshot: %d stars, %v", len(doc.Stars), err)
	}
}

func TestStarBackupCapacityAndStalePreviewAreAtomic(t *testing.T) {
	store := &starTestStore{}
	book, mux, events := newStarTestBook(store)
	ctx := t.Context()
	doc := starCollection{Revision: 1}
	for index := range StarredMax - 1 {
		doc.Stars = append(doc.Stars, Star{ID: fmt.Sprintf("sm%032x", index+100),
			StarInput: testStar(fmt.Sprint(index)), StarredAt: backupMessage(1).StarredAt})
	}
	if ok, err := store.CompareSetting(ctx, StarredKey, 0, doc); err != nil || !ok {
		t.Fatalf("seed snapshots: %v, %v", ok, err)
	}

	messages := []starBackupMessage{backupMessage(1), backupMessage(2)}
	var preview starBackupPreview
	doStar(t, mux, "POST", "/import/preview", "full", backupJSON(t, messages, nil), 200, &preview)
	if preview != (starBackupPreview{Revision: 1, Total: 2, ToAdd: 2, Available: 1}) {
		t.Fatalf("a full batch should remain previewable: %+v", preview)
	}
	doStar(t, mux, "POST", "/import", "full", backupJSON(t, messages, &preview.Revision), 400, nil)
	unchanged, err := loadStars(ctx, store)
	if err != nil || !reflect.DeepEqual(unchanged, doc) || len(*events) != 0 {
		t.Fatalf("over-capacity batch inserted something: %d records, %v, %v", len(unchanged.Stars), *events, err)
	}

	// A normal star added after preview changes the same revision, even
	// though it never reads an archive.
	if _, err := book.add(ctx, testStar("filled-by-legacy-writer")); err != nil {
		t.Fatal(err)
	}
	doStar(t, mux, "POST", "/import", "full", backupJSON(t, messages[:1], &preview.Revision), 409, nil)
	after, err := loadStars(ctx, store)
	if err != nil || after.Revision != 2 || len(after.Stars) != StarredMax || len(*events) != 1 {
		t.Fatalf("stale preview was applied: %+v, %v, %v", after.Revision, *events, err)
	}

	// A full collection can still restore a backup that adds nothing.
	onlySaved := backupMessage(9)
	onlySaved.Key = after.Stars[0].ID
	var posted starBackupImport
	doStar(t, mux, "POST", "/import", "full", backupJSON(t, []starBackupMessage{onlySaved}, &after.Revision), 200, &posted)
	if posted != (starBackupImport{Revision: 2, AlreadySaved: 1}) || len(*events) != 1 {
		t.Fatalf("duplicate at capacity wrote or failed: %+v, %v", posted, *events)
	}
}

func TestStarBackupCompareAndSwapConflictDoesNotNotify(t *testing.T) {
	store := &starTestStore{conflict: true}
	_, mux, events := newStarTestBook(store)
	zero := int64(0)
	doStar(t, mux, "POST", "/import", "full", backupJSON(t, []starBackupMessage{backupMessage(1)}, &zero), 409, nil)
	if store.json != nil || len(*events) != 0 {
		t.Fatalf("a failed compare-and-swap changed something: %+v, %+v", store, events)
	}
}
