// The project-document service has its own member credentials. Keep this read independent of hub authentication.
export type TeamProjectDocument = {
  project: string;
  document: string;
  revision: number;
  content: string;
  sha256: string;
  author: string;
  approved_by: string;
};

type ErrorCode = "access" | "missing" | "invalid" | "unavailable";

export class TeamProjectDocumentError extends Error {
  constructor(readonly code: ErrorCode) {
    super(code);
    this.name = "TeamProjectDocumentError";
  }
}

function validId(value: unknown): value is string {
  return typeof value === "string" && /^[A-Za-z0-9_-]{1,100}$/.test(value);
}

const maxContentBytes = 256 * 1024;
// JSON can encode each content byte as six bytes (e.g. a control character).
const maxReplyBytes = 6 * maxContentBytes + 4096;

async function readReply(response: Response): Promise<unknown> {
  if (!response.body) throw new TeamProjectDocumentError("invalid");
  if (Number(response.headers.get("Content-Length")) > maxReplyBytes) {
    void response.body.cancel().catch(() => {});
    throw new TeamProjectDocumentError("invalid");
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let size = 0;
  let text = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > maxReplyBytes) {
        void reader.cancel().catch(() => {});
        throw new TeamProjectDocumentError("invalid");
      }
      text += decoder.decode(value, { stream: true });
    }
    return JSON.parse(text + decoder.decode());
  } catch {
    throw new TeamProjectDocumentError("invalid");
  } finally {
    reader.releaseLock();
  }
}

export async function loadApprovedProjectDocument(
  project: string, document: string, token: string, signal: AbortSignal,
): Promise<TeamProjectDocument> {
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  const { origin, protocol, hostname } = location;
  const loopback = hostname === "localhost" || hostname.endsWith(".localhost") || hostname === "[::1]" ||
    /^127(?:\.\d{1,3}){3}$/.test(hostname);
  if (!(protocol === "https:" || (protocol === "http:" && loopback)) || !globalThis.crypto?.subtle) {
    throw new TeamProjectDocumentError("unavailable");
  }
  if (!validId(project) || !validId(document)) throw new TeamProjectDocumentError("invalid");
  if (!/^[A-Za-z0-9_-]+$/.test(token)) throw new TeamProjectDocumentError("access");

  try {
    const response = await fetch(new URL(`/v1/projects/${project}/documents/${document}/approved`, origin), {
      method: "GET", headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
      credentials: "omit", mode: "same-origin", cache: "no-store", redirect: "error", referrerPolicy: "no-referrer", signal,
    });
    if (response.status === 401 || response.status === 403) throw new TeamProjectDocumentError("access");
    if (response.status === 404) throw new TeamProjectDocumentError("missing");
    if (response.status !== 200) throw new TeamProjectDocumentError("unavailable");
    const row = await readReply(response);
    if (!row || typeof row !== "object") throw new TeamProjectDocumentError("invalid");
    const doc = row as Partial<TeamProjectDocument>;
    if (doc.project !== project || doc.document !== document || !validId(doc.approved_by) || !validId(doc.author) ||
      typeof doc.revision !== "number" || !Number.isSafeInteger(doc.revision) || doc.revision < 1 ||
      typeof doc.content !== "string" || !doc.content.length || doc.content.length > maxContentBytes ||
      /[\u0000\uD800-\uDFFF]/u.test(doc.content) || typeof doc.sha256 !== "string" || !/^[a-f0-9]{64}$/.test(doc.sha256)) {
      throw new TeamProjectDocumentError("invalid");
    }
    const bytes = new TextEncoder().encode(doc.content);
    if (bytes.length > maxContentBytes) throw new TeamProjectDocumentError("invalid");
    const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
    const digest = Array.from(hash, byte => byte.toString(16).padStart(2, "0")).join("");
    if (digest !== doc.sha256) throw new TeamProjectDocumentError("invalid");
    if (signal.aborted) throw new DOMException("Aborted", "AbortError");
    return { project, document, revision: doc.revision, content: doc.content, sha256: digest, author: doc.author, approved_by: doc.approved_by };
  } catch (err) {
    if (signal.aborted) throw new DOMException("Aborted", "AbortError");
    if (err instanceof TeamProjectDocumentError) throw err;
    throw new TeamProjectDocumentError("unavailable");
  }
}
