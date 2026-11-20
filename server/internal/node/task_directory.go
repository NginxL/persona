package node

import (
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/i18n"
	"io"
	"os"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// taskDirectory runs on the execution computer. It never prepares, creates or chmods a user directory.
func (n *Node) taskDirectory(path string) (proto.Workspace, error) {
	if path != strings.TrimSpace(path) {
		return proto.Workspace{}, i18n.Err("assistant-schedule.execution_directory_whitespace")
	}
	w, err := n.resolveWorkspace(path, false)
	if err != nil {
		return w, err
	}
	// Opening '/.' checks search access to the directory as well as read access, without changing process cwd.
	f, err := os.Open(w.Cwd + string(os.PathSeparator) + ".")
	if err != nil {
		return w, err
	}
	defer f.Close()
	if _, err = f.ReadDir(1); err != nil && !errors.Is(err, io.EOF) {
		return w, err
	}
	return w, nil
}

func (n *Node) checkTaskDirectory(id, cwd, launcher string) error {
	w, err := n.taskDirectory(cwd)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	st, ok := n.state.get(id)
	launcher = launcherOr(launcher, st.Launcher)
	if row, owner, listed := n.runtimes.rowOf(id); listed {
		previous := row.Launcher
		if previous == "" {
			owner.mu.Lock()
			previous = owner.hello.Launcher
			owner.mu.Unlock()
		}
		// Match the sender's stored-launcher precedence before falling back to the runtime owner.
		launcher = launcherOr(launcher, previous)
		if !owner.assistant() || n.assistantRuntime(launcher) != owner || previous != launcher {
			return errors.New("task execution context differs from this conversation; start a new conversation")
		}
		st.Cwd, st.Launcher, ok = row.Cwd, previous, true
	}
	if !ok {
		if _, _, known := n.runtimes.belongs(id); known {
			return i18n.Err("assistant-schedule.execution_context_unavailable") // known ownership is not proof of a gone conversation
		}
		return fmt.Errorf("conversation %s is gone", id)
	}
	previous, err := n.taskDirectory(st.Cwd)
	if err != nil {
		return err
	}
	if previous.Cwd != w.Cwd || st.Launcher != launcher {
		return errors.New("task execution context differs from this conversation; start a new conversation")
	}
	return nil
}

// Ordinary assistant starts may create their managed directory. Task execution only validates the chosen directory.
func (n *Node) prepareAssistantCwd(cwd string, strict bool) error {
	if strict {
		_, err := n.taskDirectory(cwd)
		return err
	}
	return os.MkdirAll(cwd, 0o700)
}

// assistantDirectoryPrepared identifies directories whose generated assistant files the hub owns.
// A user-selected task directory keeps its own instructions and skills.
func (n *Node) assistantDirectoryPrepared(cwd string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.assistantPrepared[assistantPrepareDir(cwd)]
	return ok
}
