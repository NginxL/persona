package assistantwire

// The read marks of a chat's answers (#13021, docs/protocol/protocol.md "Question reads"): the web's question rail
// keeps which answers the owner opened on the manager (assistant/question_reads.go), and the hub relays these routes.
var questionReadRoutes = []string{
	"GET /api/v1/sessions/{mid}/{sid}/question-reads",
	"POST /api/v1/sessions/{mid}/{sid}/question-reads",
}

func init() { Routes = append(Routes, questionReadRoutes...) }
