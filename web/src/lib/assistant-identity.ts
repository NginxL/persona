// The sidebar head and conversation title share the same live or remembered assistant identity.
// Display only: the remembered identity never changes routing or the current manager.
import { pickedName, useChosenManager, useShownIdentity, useShownManager, type ShownIdentity } from "./manager-choice";
import { managerOf, useRoster } from "./managers";

export function useAssistantIdentity(): ShownIdentity | null {
  const roster = useRoster();
  const current = useShownManager();
  const remembered = useShownIdentity();
  const picked = useChosenManager();
  const record = managerOf(roster, current);
  const known = record?.installing ? null : record;
  const kept = picked ? { id: picked, name: remembered?.id === picked ? remembered.name : pickedName() ?? picked } : remembered;
  return known ?? (!roster ? kept : null);
}
