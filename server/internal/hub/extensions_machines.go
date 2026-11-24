package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// hubMachinesPairLink handles extwire.HubMachinesPairLink from an extension granted machines.pair (#12798).
// It reuses the device pairing-code flow (fromDevice = true): newly added computers are always pending until the
// owner confirms "This is my computer", and already-confirmed computers are never given a new code.
func (h *Hub) hubMachinesPairLink(ctx context.Context, from, extID string, raw json.RawMessage) (wire.MachinesPairLinkResult, error) {
	var p wire.MachinesPairLinkParams
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return wire.MachinesPairLinkResult{}, &rpc.Error{Code: "invalid_params", Message: err.Error()}
		}
	}
	if h.isMember() {
		return wire.MachinesPairLinkResult{}, &rpc.Error{Code: "mesh_member", Message: "pairing computers through a member hub is not supported"}
	}
	if h.store == nil {
		return wire.MachinesPairLinkResult{}, &rpc.Error{Code: "unavailable", Message: "no database"}
	}
	if !h.linkPairable() {
		return wire.MachinesPairLinkResult{}, &rpc.Error{Code: "tls_required", Message: "hub TLS or https public_url is required for Persona Link pairing"}
	}
	machineID := strings.TrimSpace(p.MachineID)
	name := strings.TrimSpace(p.Name)
	caller := from
	if caller == "" {
		caller = devicesOwner
	}
	if err := h.addTarget(ctx, from); err != nil {
		return wire.MachinesPairLinkResult{}, mapAddMachineRPCError(err)
	}
	actor := "extension:" + extID
	h.machineAdd.Lock()
	var add *machineAddRequest
	var existingMachine string
	if machineID != "" && h.knownEntry(machineID) != nil && name == "" && p.OS == "" && p.Icon == "" {
		existingMachine = machineID
	} else {
		addName := name
		if addName == "" && machineID != "" && adminMachineID.MatchString(machineID) {
			addName = machineID
		}
		add = &machineAddRequest{
			ID:      machineID,
			Name:    addName,
			OS:      p.OS,
			Icon:    p.Icon,
			Manager: from,
		}
	}
	id, _, err := h.computerForScoped(ctx, existingMachine, add, true, actor, caller)
	h.machineAdd.Unlock()
	if err != nil {
		return wire.MachinesPairLinkResult{}, mapAddMachineRPCError(err)
	}
	code, err := h.newPairCode(ctx, id)
	if err != nil {
		return wire.MachinesPairLinkResult{}, err
	}
	h.notifyComputerCode(principal{ID: actor, Name: extID, Role: "extension", Extension: extID}, id)
	displayName := id
	if mc := h.knownEntry(id); mc != nil && mc.Name != "" {
		displayName = mc.Name
	}
	installLinux := ""
	if inst := h.computerInstall(code.PairURL); inst != nil {
		installLinux = inst["linux"]
	}
	return wire.MachinesPairLinkResult{
		MachineID:    id,
		Name:         displayName,
		Link:         code.PairURL,
		ExpiresAt:    code.ExpiresAt.UTC(),
		Pending:      h.machinePending(id),
		InstallLinux: installLinux,
	}, nil
}

// hubMachinesStatus handles extwire.HubMachinesStatus from an extension granted machines.read or machines.pair (#12798).
func (h *Hub) hubMachinesStatus(from string, raw json.RawMessage) (wire.MachinesStatusResult, error) {
	var p wire.MachinesStatusParams
	if len(bytes.TrimSpace(raw)) == 0 {
		return wire.MachinesStatusResult{}, &rpc.Error{Code: "invalid_params", Message: "machine_id is required"}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return wire.MachinesStatusResult{}, &rpc.Error{Code: "invalid_params", Message: err.Error()}
	}
	id := strings.TrimSpace(p.MachineID)
	if id == "" {
		return wire.MachinesStatusResult{}, &rpc.Error{Code: "invalid_params", Message: "machine_id is required"}
	}
	caller := from
	if caller == "" {
		caller = devicesOwner
	}
	if h.knownEntry(id) != nil && h.entryOwner(id) != caller {
		return wire.MachinesStatusResult{}, &rpc.Error{Code: codeOtherManager, Message: errOtherManager.Error()}
	}
	res := wire.MachinesStatusResult{
		MachineID: id,
		Pending:   h.machinePending(id),
	}
	if mc := h.knownEntry(id); mc != nil {
		res.Exists = true
		res.Name = mc.Name
		if res.Name == "" {
			res.Name = mc.ID
		}
	}
	for _, m := range h.machineList() {
		if m.ID != id {
			continue
		}
		if h.entryOwner(m.ID) != caller {
			continue
		}
		res.Exists = true
		if m.Name != "" {
			res.Name = m.Name
		}
		res.Online = m.Online || m.Link != nil
		res.NodeOnline = m.Online
		res.Error = m.Error
		if !m.LastSeen.IsZero() {
			res.LastSeen = m.LastSeen.UTC()
		}
		break
	}
	return res, nil
}

func mapAddMachineRPCError(err error) error {
	var ae *addMachineError
	if errors.As(err, &ae) && ae.code != "" {
		return &rpc.Error{Code: ae.code, Message: ae.err.Error()}
	}
	return err
}
