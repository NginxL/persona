package assistant

import (
	"net/http"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// socketRoutes are the assistant's WebSocket routes (assistantwire.SocketRoutes), served by ServeSocket: the A2A
// sockets, and the recording module's audio socket while the module is loaded (recording.go).
var socketRoutes = []struct {
	pattern string
	serve   func(s *Service, w http.ResponseWriter, r *http.Request)
}{
	{"GET /api/v1/recordings/{id}/audio", (*Service).recordingAudioSocket},
	{assistantwire.A2ARPC, (*Service).a2aRPC},
	{assistantwire.A2ACard, (*Service).a2aCard},
}

// ServeSocket serves the assistant's WebSocket routes (assistantwire.SocketRoutes), which the hub relays to the
// manager's extensions layer and that hands here (extmgr.Options.AssistantSocket).
func (s *Service) ServeSocket(w http.ResponseWriter, r *http.Request) {
	s.sockets.Do(func() {
		s.socketMux = http.NewServeMux()
		for _, sr := range socketRoutes {
			s.socketMux.HandleFunc(sr.pattern, func(w http.ResponseWriter, r *http.Request) { sr.serve(s, w, r) })
		}
	})
	s.socketMux.ServeHTTP(w, r)
}
