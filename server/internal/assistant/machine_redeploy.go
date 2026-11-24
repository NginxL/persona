package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type machineRedeployer interface {
	Redeploy(ctx context.Context, id string) error
}

func (s *Service) toolMachineRedeploy(ctx context.Context, currentMachine string, args json.RawMessage) (string, error) {
	var a struct {
		Machine string `json:"machine"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	selector := strings.TrimSpace(a.Machine)
	if selector == "" {
		return "", fmt.Errorf("machine is required")
	}
	var listed struct {
		Devices  []Device `json:"devices"`
		Complete bool     `json:"complete"`
	}
	if err := s.call(ctx, DeviceAll, DeviceDevicesList, map[string]any{}, &listed); err != nil {
		return "", fmt.Errorf("machine list unavailable: %w", err)
	}
	var target Device
	found := false
	for _, machine := range listed.Devices {
		if machine.ID == selector {
			target, found = machine, true
			break
		}
	}
	if !found && !listed.Complete {
		return "", fmt.Errorf("machine list is incomplete; use a machine ID")
	}
	if !found {
		for _, machine := range listed.Devices {
			if machine.Name != selector {
				continue
			}
			if found {
				return "", fmt.Errorf("machine name %q is ambiguous; use its ID", selector)
			}
			target, found = machine, true
		}
	}
	if !found {
		return "", fmt.Errorf("no machine %q", selector)
	}
	if target.ID == currentMachine {
		return "", fmt.Errorf("cannot redeploy the machine running this conversation; call this tool from another machine")
	}
	d, ok := s.physicalDevices().(machineRedeployer)
	if !ok {
		return "", fmt.Errorf("machine redeployment is unavailable")
	}
	if err := d.Redeploy(ctx, target.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Redeploy of %s (%s) was successful.", target.Name, target.ID), nil
}
