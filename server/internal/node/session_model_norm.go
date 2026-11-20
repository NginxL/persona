package node

import (
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// Session rows carry the model a transcript reports, including any upstream gateway routing prefix ("route-a/model-x",
// "passthrough/vendor/model"), so clients show which channel actually served the turn while RequestedModel keeps the
// launcher's requested id for carry-over (docs/lessons/model-display.md, #10125). normalizeSessionRowModel trims white
// space and clears stand-in placeholders such as "<synthetic>".
func (n *Node) normalizeSessionRowModel(s *proto.Session) {
	if s == nil {
		return
	}
	s.Model = strings.TrimSpace(s.Model)
	if isBracketedModel(s.Model) {
		s.Model = ""
	}
}

// isBracketedModel: a stand-in marker such as "<synthetic>", not a model id.
func isBracketedModel(id string) bool {
	return strings.HasPrefix(id, "<") && strings.HasSuffix(id, ">")
}
