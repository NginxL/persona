package assistant

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// What the recording module answers and names, as the core's integration tests expect it (the module's own code
// says the same: internal/modules/recording).
const (
	codeRecordingNotFound = "recording_not_found"
	codeTranscriptExpired = "transcript_expired"
	problemNoInterim      = "no_interim"
	problemUpstreamFailed = "upstream_failed"
	attendeeUnavailable   = "attendee_unavailable"
	attendeeNotAllowed    = "attendee_not_allowed"
	attendeeBusy          = "busy"
	dayPushPhone          = "app" // channel.App: the phone among a day push's destinations
	partRoom              = "room"
	titleMeeting          = "会议"            // i18n-ignore: a meeting conversation title's kind
	justMeMax             = 3 * time.Hour   // how long a Just me turn's mark lasts at most
	justMeAfter           = 2 * time.Minute // and after its turn ended
)

// The module's codes, limits and times its integration tests read (its code says the same).
const (
	codeNotDay                 = "not_day"
	codeBaseChanged            = "base_changed"
	codeSegmentsGap            = "segments_gap"
	codeDayFull                = "day_full"
	codeKindChanged            = "kind_changed"
	maxSegmentsBody            = 1 << 20
	codeMinutesStale           = "minutes_stale"
	askPending                 = "pending"
	askAnswered                = "answered"
	askFailed                  = "failed"
	askTyped                   = "typed"
	askModelUnavailable        = "model_unavailable"
	askModelError              = "model_error"
	askModelTimeout            = "model_timeout"
	askInterrupted             = "interrupted"
	askNothingHeard            = "nothing_heard"
	maxAsks                    = 100
	maxAskQuestionRunes        = 1000
	maxAnswerRunes             = 400
	codeBadFrame               = "bad_frame"
	codeReplaced               = "replaced"
	codeNotHub                 = "not_hub"
	codeTranscriptionFinal     = "transcription_final"
	codeHubTranscribes         = "hub_transcribes"
	codeBadAudio               = "bad_audio"
	maxAudioBody               = 10 << 20
	maxMeetingTextRunes        = 8000
	feedBatchEvery             = time.Minute
	feedBatchRunes             = 12000
	feedEndWait                = 3 * time.Minute
	feedBusyPoll               = 20 * time.Second
	feedRetry                  = time.Minute
	feedKickDelay              = time.Second
	feedStale                  = 12 * time.Hour
	feedAskAnswered            = "answered"
	handoffNotAgent            = "not_agent"
	handoffNotAllowed          = "not_allowed"
	handoffUnavailable         = "unavailable"
	handoffSendFailed          = "send_failed"
	codeNotAttending           = "not_attending"
	codeLiveGap                = "live_gap"
	codeLiveFull               = "live_full"
	codeOpIDReused             = "op_id_reused"
	codeAsksFull               = "asks_full"
	codeAskBusy                = "ask_busy"
	codeMeetingEnded           = "meeting_ended"
	codeNotMeeting             = "not_meeting"
	codeMeetingRunning         = "meeting_running"
	codeNoMaterial             = "no_material"
	maxLiveBody                = 1 << 20
	maxAskBody                 = 64 << 10
	maxLiveLines               = 500
	maxPhotoBody               = 2 << 20
	codePhotoNotFound          = "photo_not_found"
	codePhotoDeleted           = "photo_deleted"
	codePhotosFull             = "photos_full"
	photoSendTries             = 3
	partFollowup               = "followup"
	askNoAnswer                = "no_answer"
	askRoomUnavailble          = "room_unavailable"
	roomLeanEvery              = 2 * time.Minute
	roomListsEvery             = 30 * time.Minute
	roomCompactSpan            = 45 * time.Minute
	roomCompactRetry           = 5 * time.Minute
	roomCompactLost            = 10 * time.Minute
	toolTopicsSet              = "meeting_topics_set"
	toolHighlightsSet          = "meeting_highlights_set"
	problemUpstreamForbidden   = "upstream_forbidden"
	problemUpstreamBusy        = "upstream_busy"
	problemUpstreamUnavailable = "upstream_unavailable"
	meetingTurnStartWithin     = 10 * time.Minute
	meetingTurnRunMax          = 3 * time.Hour
)

// meetingTurnTools are the tools a meeting's turn may call (the module's meeting_turns.go).
var meetingTurnTools = []string{"kb_search", "kb_list", "kb_get", "todo_list", "todo_get", "todo_repositories_get", "agents_list", "agent_read",
	"conversations_list", "conversation_read", "machines_list", "list_followups"}

// meetingSessionID is the conversation id the module gives part ("room", "followup") of recording's meeting.
func meetingSessionID(recording, part string) string {
	sum := sha256.Sum256([]byte("persona-meeting:" + part + ":" + strings.ToLower(recording)))
	sum[6], sum[8] = sum[6]&0x0f|0x40, sum[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// askMsgID is the client_msg_id the module gives attempt k of ask id in a meeting's room.
func askMsgID(id string, k int) string { return fmt.Sprintf("ask-%s-%d", id, k) }
