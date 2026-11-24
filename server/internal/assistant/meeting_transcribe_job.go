package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/transcribejob"
)

// dialJob opens a job of transcription contribution ref (internal/transcribejob): a meeting's live and final passes
// (the recording module, through its host) and an extension's own audio (extension_transcribe.go).
func (s *Service) dialJob(ctx context.Context, ref string) (*transcribejob.Conn, error) {
	if s.trans == nil {
		return nil, transcribejob.ErrNoTranscribers
	}
	c, done, err := s.trans.TranscriptionDial(ctx, ref)
	if err != nil {
		return nil, err
	}
	return transcribejob.New(c, done), nil
}
