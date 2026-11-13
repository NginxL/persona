/** Resolve an authenticated roster before opening any manager-owned state. */
export function resolveManagerScope(requested: string, defaultId: string, managerIds: readonly string[]): string {
  if (!defaultId || !managerIds.includes(defaultId)) throw new Error('invalid_manager_roster');
  return requested && managerIds.includes(requested) ? requested : defaultId;
}
