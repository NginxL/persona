package nodecmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/nwverse/persona/server/internal/node"
	"github.com/nwverse/persona/server/internal/nodeupdate"
)

// residentCheckLimits bound the replacement's node-update-check before a reload or at startup. A new file gets a
// first run long enough for endpoint protection to scan it (#12528); a file that already passed here gets 5 s.
var residentCheckLimits = nodeupdate.CheckLimits{FirstRun: 60 * time.Second, Repeat: 5 * time.Second}

func checkResidentReplacement(ctx context.Context, path, program string) (*nodeupdate.Check, error) {
	check, err := residentReplacementInfo(ctx, path, program)
	if err != nil {
		return check, err
	}
	// Where there is no handover (Windows), the supervisor starts the replacement as a new process instead.
	if !check.ResidentHandover && node.ResidentHandoverSupported {
		return check, errors.New("resident replacement does not support listener handover")
	}
	return check, nil
}

func preflightResidentReload(ctx context.Context, logger *log.Logger, path, program string) error {
	check, err := checkResidentReplacement(ctx, path, program)
	if err == nil {
		return nil
	}
	var identity string
	if check != nil {
		identity = fmt.Sprintf(" (version=%q os=%q arch=%q program=%q resident_handover=%t)",
			check.Version, check.OS, check.Arch, check.Program, check.ResidentHandover)
	}
	logger.Printf("resident reload: replacement did not pass preflight; keeping the current node: %v%s", err, identity)
	return err
}

// residentReplacementInfo returns the reported identity even if it fails validation.
func residentReplacementInfo(ctx context.Context, path, program string) (*nodeupdate.Check, error) {
	info, err := nodeupdate.RunCheck(ctx, path, residentCheckLimits)
	if err != nil {
		return nil, err
	}
	check := &info
	if check.OS != runtime.GOOS || check.Arch != runtime.GOARCH || check.Version == "" ||
		(check.Program != "" && program != "" && check.Program != program) {
		return check, errors.New("resident replacement identity does not match this node")
	}
	return check, nil
}

// Startup rollback has not served a connection yet. Legacy images must receive neither the listener nor the lock;
// they reacquire both themselves. A capable image can use the normal descriptor handover.
func prepareResidentRecovery(transport *node.ResidentTransport, handover bool) (*node.ResidentFiles, error) {
	if !handover {
		transport.Close()
		return nil, nil
	}
	return transport.PrepareExec()
}
