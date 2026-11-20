//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// runAgentExec mounts each configured alias symlink itself, rather than its target, and then execs the agent. bwrap
// leaves this process as the agent's direct child; exec preserves that pid and the node's existing parent/child model.
func runAgentExec(args []string) error {
	if len(args) < 2 || args[0] != "--" {
		return errors.New("agent sandbox exec: missing command")
	}
	var aliases []string
	for i := 0; ; i++ {
		key := agentAliasEnv(i)
		alias, ok := os.LookupEnv(key)
		if !ok {
			if i == 0 {
				return errors.New("agent sandbox exec: no data-directory aliases")
			}
			break
		}
		if err := os.Unsetenv(key); err != nil {
			return err
		}
		aliases = append(aliases, alias)
	}
	for _, alias := range aliases {
		if err := mountAgentAlias(alias); err != nil {
			return err
		}
	}
	program, err := exec.LookPath(args[1])
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return fmt.Errorf("agent sandbox exec %q: %w", args[1], err)
	}
	if err := unix.Exec(program, args[1:], os.Environ()); err != nil {
		return fmt.Errorf("agent sandbox exec %q: %w", args[1], err)
	}
	return nil
}

// mountAgentAlias uses the new mount API because bwrap's pathname mounts follow the final symlink. A detached clone of
// the symlink inode moved onto itself without following it makes unlink and rename fail with EBUSY. Resolving the link
// for a read still reaches its target and therefore the masked data-directory view. As with the other bwrap mounts, an
// agent that deliberately uses its namespace's mount-admin capability can unmount this view; the original capabilities
// stay unchanged so nested-namespace tools keep working.
func mountAgentAlias(alias string) error {
	info, err := os.Lstat(alias)
	if err != nil {
		return fmt.Errorf("agent sandbox alias %q: %w", alias, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("agent sandbox alias %q is not a symlink", alias)
	}
	fd, err := unix.OpenTree(unix.AT_FDCWD, alias, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_SYMLINK_NOFOLLOW)
	if err != nil {
		return fmt.Errorf("agent sandbox alias %q open_tree: %w", alias, err)
	}
	defer unix.Close(fd)
	if err := unix.MoveMount(fd, "", unix.AT_FDCWD, alias, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("agent sandbox alias %q move_mount: %w", alias, err)
	}
	return nil
}
