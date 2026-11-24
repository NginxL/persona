package node

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// The hub in office network mode stops this process's tailcat endpoint once and for good; with none, nothing happens.
func TestStopTailcat(t *testing.T) {
	stopTailcat() // none served: nothing to stop
	var logged bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailcatStop.Store(&tailcatEndpoint{cancel: cancel, logger: log.New(&logged, "", 0)})
	stopTailcat()
	if ctx.Err() == nil {
		t.Fatal("the endpoint's context is still live")
	}
	if !strings.Contains(logged.String(), "office network mode") {
		t.Fatalf("log %q", logged.String())
	}
	if tailcatStop.Load() != nil {
		t.Fatal("the endpoint is still recorded")
	}
}
