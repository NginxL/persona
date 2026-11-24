package github

import (
	"testing"

	"github.com/nwverse/persona/server/internal/connector/conformance"
)

// GitHub is one implementation of the connector protocol: what it gives directly, it gives through the wire.
func TestConformance(t *testing.T) {
	c, _ := fakeArchiveAPI(t)
	conformance.Check(t, c)
}
