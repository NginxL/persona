/** Only explicit refusal evidence permits replacing a dispatched write. */
export function writeResultUnknown(status: number, body = ""): boolean {
  if (body) {
    try {
      const data = JSON.parse(body) as Record<string, unknown>;
      if (data.state === "unknown" || data.state === "pending" || data.delivered === "unknown" || data.delivered === "yes") return true;
      if (data.delivered === "none") return false;
    } catch { /* An unreadable response is not proof of no side effect. */ }
  }
  return ![400, 401, 403, 404, 405, 409, 410, 413, 415, 422].includes(status);
}
