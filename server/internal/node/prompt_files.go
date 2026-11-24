package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/promptcontent"
	"github.com/nwverse/persona/server/internal/prompts"
)

type taskInputKey struct{}

func checkFileReferenceAudience(ctx context.Context, text, origin string, guest, public bool) error {
	if guest || public || origin == originInbox {
		if refs, err := promptcontent.Files(text); err != nil || len(refs) > 0 {
			return fileSelectionUnavailable(ctx)
		}
	}
	return nil
}

func fileSelectionUnavailable(ctx context.Context) error {
	return fileSelectionRefusal(ctx, "assistant-schedule.selected_file_unavailable")
}

// These content refusals occur before delivery and require a new selection or a different execution context.
func fileSelectionRefusal(ctx context.Context, messageKey string) error {
	return refusal("file_unavailable", i18n.T(i18n.From(ctx), messageKey), map[string]any{"retryable": false})
}

// selectedFiles uses the same path guard as the file viewer, on the executing node, on every turn.
// Files are references to current content, never stored snapshots or permission exemptions.
func (n *Node) selectedFiles(ctx context.Context, text, cwd, launcher, runtime string) ([]prompts.AssistantSelectedFile, error) {
	refs, err := promptcontent.Files(text)
	if err != nil {
		return nil, fileSelectionUnavailable(ctx)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if ctx.Value(taskInputKey{}) != true {
		return nil, fileSelectionUnavailable(ctx)
	}
	if n.assistantRuntime(launcher) != nil {
		return nil, fileRuntimeUnsupported(ctx)
	}
	n.mu.Lock()
	machine := n.machineID
	n.mu.Unlock()
	base, _, err := n.statSafe(cwd)
	if err != nil {
		return nil, fileSelectionUnavailable(ctx)
	}
	var out []prompts.AssistantSelectedFile
	for _, ref := range refs {
		if ref.Machine != machine || ref.Cwd != cwd || ref.Launcher != launcher || ref.Runtime != runtime {
			return nil, fileSelectionUnavailable(ctx)
		}
		path := n.resolvePath(ref.Path, cwd)
		if path == "" || strings.TrimSpace(ref.Path) != ref.Path {
			return nil, fileSelectionUnavailable(ctx)
		}
		real, st, e := n.statSafe(path)
		if e != nil || !st.Mode().IsRegular() {
			return nil, fileSelectionUnavailable(ctx)
		}
		if !withinTaskDirectory(base, real) || strings.ContainsAny(real, "\r\n") {
			return nil, fileSelectionUnavailable(ctx)
		}
		f, e := os.Open(real)
		if e != nil {
			return nil, fileSelectionUnavailable(ctx)
		}
		_ = f.Close()
		out = append(out, prompts.AssistantSelectedFile{Name: filepath.Base(real), Path: real})
	}
	return out, nil
}

func fileRuntimeUnsupported(ctx context.Context) error {
	return fileSelectionRefusal(ctx, "assistant-schedule.file_runtime_unsupported")
}

func (n *Node) validatePromptContent(ctx context.Context, id, cwd, launcher, text string, execution modelExecution) error {
	st, _ := n.state.get(id)
	origin, _ := assistantInputOrigin(st, execution, id != "")
	if err := checkFileReferenceAudience(ctx, text, origin, st.Guest != nil, execution.Public); err != nil {
		return err
	}
	strictRuntime := n.strictRuntimeSession(id, execution.Strict)
	if onRuntime, runs := n.assistantRuntimeRoute(id, launcherOr(launcher, st.Launcher), strictRuntime); onRuntime != nil || runs {
		// The picker also checks this with no prompt, before it offers selections.
		return fileRuntimeUnsupported(ctx)
	}
	if _, err := n.selectedFiles(ctx, text, cwd, launcher, execution.Runtime); err != nil {
		return err
	}
	_, err := n.selectedSkills(ctx, id, cwd, launcher, text, execution)
	return err
}
