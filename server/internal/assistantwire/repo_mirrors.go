package assistantwire

// repoMirrorRoutes are the opt-in managed bare repository mirror APIs (#13197).
// All mirror routes require full credentials (no extension scopes).
var repoMirrorRoutes = []string{
	"GET /api/v1/assistant/repositories/mirrors",
	"GET /api/v1/assistant/repositories/{id}/mirrors/{machine_id}",
	"PUT /api/v1/assistant/repositories/{id}/mirrors/{machine_id}",
	"POST /api/v1/assistant/repositories/{id}/mirrors/{machine_id}/sync",
	"DELETE /api/v1/assistant/repositories/{id}/mirrors/{machine_id}",
}

func init() { Routes = append(Routes, repoMirrorRoutes...) }
