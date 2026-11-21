import type { HerdrMachine } from "./herdr-tree";

/** The old activity-sort entry point retains the machine hierarchy's stable herdr order (#11801). */
export function machineTreeByUpdate(tree: readonly HerdrMachine[]): HerdrMachine[] {
  return [...tree];
}
