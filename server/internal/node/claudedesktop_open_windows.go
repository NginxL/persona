package node

import (
	"context"
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Both desktop builds register claude://, including the packaged app whose handler has no shell command.
var openDesktopLink = openClaudeDesktopLink

var claudeDesktopLinkRegistered = func() bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, "claude", registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	_, _, err = k.GetStringValue("URL Protocol")
	k.Close()
	return err == nil && (assocString("claude", assocStrAppID) != "" || assocString("claude", assocStrCommand) != "")
}

// A variable so tests never activate the user's desktop app.
var shellOpenClaudeDesktop = shellOpen

func openClaudeDesktopLink(ctx context.Context, link string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	const prefix = "claude://claude.ai/epitaxy/local_"
	const resume = "claude://resume?session="
	valid := strings.HasPrefix(link, prefix) && validID(strings.TrimPrefix(link, prefix))
	valid = valid || strings.HasPrefix(link, resume) && validID(strings.TrimPrefix(link, resume))
	if !valid {
		return errors.New("invalid Claude desktop session link")
	}
	if !claudeDesktopLinkRegistered() {
		return errors.New("claude:// is not registered")
	}
	return shellOpenClaudeDesktop(link)
}
