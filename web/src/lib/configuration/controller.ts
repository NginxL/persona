// The state of one configuration form (contract.ts) over one injected source: reading, what a person is editing, one
// write at a time against the revision they saw, and what came back. Framework-free (ConfigurationForm.tsx draws it) so
// its timing can be tested on its own.
//
// - Nothing shows as saved before the source has committed and returned the new Document; a choice being written shows
//   as pending, and a failure puts back what the Document says.
// - Edits that take more than one step are drafts until Save: typed text and numbers, and a multiselect's checks (so a
//   one-of-one choice can move from A to B without an invalid write in between). A draft survives a failed write, a
//   conflict, a re-read and a change made elsewhere; one edited while its save is on the way is kept too.
// - A conflict, or a write whose outcome is unknown (configuration_unavailable, a request that threw or timed out, or a
//   successful answer that isn't a Document), replaces nothing: the form says so and writes again only after an explicit
//   reload, keeping every draft. A refusal the source names (a validation or storage failure) is retriable at once. An
//   unknown version blocks writing altogether.
// - A controller belongs to one source: a different source is a new controller, and a disposed one ignores every late
//   answer. A read that started before a newer Document arrived is ignored too, and so is one that started before a write
//   (or while it was in flight): it may describe the resource before that write's outcome and must not replace the
//   document the write returned or clear the barrier it set.
import {
  checkUpdate,
  checkValue,
  fieldValue,
  sameValue,
  CONFIGURATION_VERSION,
  ConfigurationError,
  parseDocument,
  RELOAD_CODES,
  safeNumber,
  type ConfigurationDocument,
  type ConfigurationSource,
  type ConfigurationUpdate,
  type FieldValue,
  type KnownField,
} from "./contract";
import { changeGuidance, invalidConditions, type ConfigurationGuidance, type ConfirmationCopy } from "./guidance";

export interface PendingConfirmation {
  fieldId: string;
  copy: ConfirmationCopy;
  update: ConfigurationUpdate;
  pending?: FieldValue;
}

export interface Problem {
  code: string;
  message?: string;
}

export interface FieldState {
  /** Typed and not saved yet (text and number fields), as typed. */
  draft?: string;
  /** A multiselect's checks not saved yet. */
  selection?: string[];
  /** A choice being written; shown as pending until the source answers. */
  pending?: FieldValue;
  error?: Problem;
  /** A confirmed save, optionally with the module’s localized receipt. */
  saved?: true | string;
}

export interface FormState {
  phase: "loading" | "ready" | "failed";
  doc: ConfigurationDocument | null;
  /** False for a schema version this client (or the writer) doesn't take: shown, never written. */
  writable: boolean;
  /** Why the Document couldn't be read (phase "failed"), or couldn't be read again (kept, phase "ready"). */
  readError: Problem | null;
  /** The field being written (one write at a time: each is against the current revision). */
  busy: string | null;
  confirmation: PendingConfirmation | null;
  /** A guided form awaiting the latest permissions and values. */
  refreshing: boolean;
  /** Writing waits for an explicit reload: the resource changed (a conflict), or a write's outcome is unknown. */
  blocked: Problem | null;
  /** A write's failure that names no field of this Document. */
  formError: Problem | null;
  fields: Record<string, FieldState>;
}

const INITIAL: FormState = { phase: "loading", doc: null, writable: false, readError: null, busy: null, confirmation: null, refreshing: false, blocked: null, formError: null, fields: {} };

function problem(e: unknown): Problem {
  if (e instanceof ConfigurationError) return e.message ? { code: e.code, message: e.message } : { code: e.code };
  return { code: "failed" };
}

const aborted = (e: unknown) => e instanceof DOMException && e.name === "AbortError";
const sameList = (a?: string[], b?: string[]) => !!a && !!b && a.length === b.length && a.every((v, i) => v === b[i]);

/** A typed number as a value: "" or anything that isn't a finite number in the portable range is not one. */
export function parseNumber(raw: string): number | null {
  const s = raw.trim();
  if (!s) return null;
  const n = Number(s);
  return safeNumber(n) ? n : null;
}

/** What a typed draft means for `field`: its value, or the field code of what is wrong with it. */
export function draftValue(field: KnownField, raw: string): { value: FieldValue } | { code: string } {
  if (field.kind === "number") {
    const s = raw.trim();
    const n = s ? Number(s) : Number.NaN;
    if (!s || Number.isNaN(n)) return { code: "type" };
    const code = checkValue(field, n);
    return code ? { code } : { value: n };
  }
  const code = checkValue(field, raw);
  return code ? { code } : { value: raw };
}

export class ConfigurationController {
  private state: FormState = INITIAL;
  private listeners = new Set<() => void>();
  private disposed = false;
  private abort = new AbortController();
  private unsubscribe: (() => void) | null = null;
  /** Counts Documents applied: a read that started before the last one is stale. */
  private applied = 0;
  private readSeq = 0;
  /** Counts writes started and finished: a read that started before a write, or while it was in flight, is stale. */
  private writeSeq = 0;
  /** The resource changed while a write was in flight: read again once it is done. */
  private again = false;

  constructor(readonly source: ConfigurationSource, public guidance?: ConfigurationGuidance) {}

  setGuidance(guidance?: ConfigurationGuidance) {
    if (this.guidance === guidance) return;
    this.guidance = guidance;
    this.set({ confirmation: null });
  }

  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  };

  snapshot = () => this.state;

  private set(next: Partial<FormState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...next };
    this.listeners.forEach((l) => l());
  }

  /** The fields with `id`'s state replaced (members left undefined are dropped; an empty state removes the entry). */
  private field(id: string, next: FieldState | null, base = this.state.fields) {
    const fields = { ...base };
    const kept = next ? (Object.fromEntries(Object.entries(next).filter(([, v]) => v !== undefined)) as FieldState) : null;
    if (kept && Object.keys(kept).length) fields[id] = kept;
    else delete fields[id];
    return fields;
  }

  start() {
    void this.read(true);
    this.unsubscribe =
      this.source.subscribe?.(() => {
        if (this.state.busy) this.again = true;
        else void this.read(false);
      }) ?? null;
  }

  dispose() {
    this.disposed = true;
    this.abort.abort();
    this.unsubscribe?.();
    this.listeners.clear();
  }

  /** Reads the Document again (Retry, Reload after a conflict); every draft stays. */
  reload() {
    void this.read(true);
  }

  private async read(explicit: boolean) {
    const seq = ++this.readSeq;
    const applied = this.applied;
    // A request that predates a write, was made while one was in flight, or answers before that write does may
    // describe the resource before its outcome; it must not replace a committed document, drop a pending choice, or
    // clear the barrier a failed write sets. The deferred post-write read starts after the end stamp with busy clear.
    const write = this.writeSeq;
    // A notification may revoke permission even before its replacement document arrives.
    if (this.state.confirmation) this.set({ confirmation: null, refreshing: true });
    if (explicit && this.state.phase === "failed") this.set({ phase: "loading", readError: null });
    try {
      const parsed = parseDocument(await this.source.read(this.abort.signal));
      if (this.disposed || seq !== this.readSeq || applied !== this.applied || write !== this.writeSeq || this.state.busy) return;
      // the same revision is the same Document (the contract changes it with any change): leave pending marks and
      // "Saved" alone, as when a source tells of its own write
      const same = this.state.doc && parsed.doc.id === this.state.doc.id && parsed.doc.revision === this.state.doc.revision && parsed.writable === this.state.writable;
      if (!same) this.apply(parsed.doc, parsed.writable);
      this.set({ phase: "ready", refreshing: false, readError: null, ...(explicit ? { blocked: null, formError: null } : {}) });
    } catch (e) {
      if (this.disposed || this.abortedByUs(e) || seq !== this.readSeq || write !== this.writeSeq || this.state.busy) return;
      this.set(this.state.doc ? { readError: problem(e) } : { phase: "failed", readError: problem(e) });
    }
  }

  /** Whether `e` is the AbortError this controller caused by disposing. A transport's own timeout is not ours: its
   *  answer never arrived, and that is an unknown outcome rather than a silence. */
  private abortedByUs(e: unknown) {
    return this.abort.signal.aborted && aborted(e);
  }

  private apply(doc: ConfigurationDocument, writable: boolean) {
    this.applied++;
    // drafts stay (they are what the person typed or checked); pending choices and saved marks belong to the old Document
    const fields: Record<string, FieldState> = {};
    for (const [id, f] of Object.entries(this.state.fields)) {
      if (f.draft === undefined && f.selection === undefined) continue;
      fields[id] = Object.fromEntries(Object.entries({ draft: f.draft, selection: f.selection, error: f.error }).filter(([, v]) => v !== undefined));
    }
    this.state = { ...this.state, doc, writable, fields, confirmation: null };
  }

  private known(id: string): KnownField | null {
    for (const g of this.state.doc?.groups ?? []) for (const f of g.fields) if (f.id === id) return f.kind === "unsupported" ? null : f;
    return null;
  }

  /** Whether `id` can be edited at all: a known field, not read-only, in a writable Document. */
  editable(id: string): boolean {
    const f = this.known(id);
    return !!f && this.state.writable && !f.read_only && !f.disabled_reason && !invalidConditions(f, this.guidance?.fields?.[id]?.rules);
  }

  /** Whether `id` can be written now: editable, nothing else in flight, no reload owed. */
  canWrite(id: string): boolean {
    return !this.disposed && this.editable(id) && !this.state.busy && !this.state.refreshing && !this.state.blocked;
  }

  private edited(id: string, next: FieldState) {
    this.set({
      fields: this.field(id, next),
      confirmation: this.state.confirmation?.fieldId === id ? null : this.state.confirmation,
    });
  }

  setDraft(id: string, raw: string) {
    const f = this.state.fields[id] ?? {};
    this.edited(id, { ...f, draft: raw, error: undefined, saved: undefined });
  }

  /** A multiselect's checks, edited before Save. */
  setSelection(id: string, values: string[]) {
    const f = this.state.fields[id] ?? {};
    this.edited(id, { ...f, selection: values, error: undefined, saved: undefined });
  }

  discard(id: string) {
    const f = this.state.fields[id];
    if (!f) return;
    this.edited(id, { ...f, draft: undefined, selection: undefined, error: undefined });
  }

  /** Saves a draft (typed text, a number, a multiselect's checks), after checking it here first. */
  saveDraft(id: string) {
    const field = this.known(id);
    const f = this.state.fields[id] ?? {};
    if (!field || !this.canWrite(id)) return;
    let value: FieldValue;
    if (field.kind === "multiselect") {
      if (!f.selection) return;
      value = f.selection;
    } else {
      if (f.draft === undefined) return;
      const r = draftValue(field, f.draft);
      if ("code" in r) {
        this.set({ fields: this.field(id, { ...f, error: { code: r.code } }) });
        return;
      }
      value = r.value;
    }
    this.write(id, { changes: { [id]: value } });
  }

  /** Writes a single-step choice (a switch, a selection) at once; it shows as pending until committed. */
  choose(id: string, value: FieldValue) {
    if (!this.canWrite(id)) return;
    this.write(id, { changes: { [id]: value } }, value);
  }

  /** Back to the default: the override is removed (the source decides what the default is). */
  reset(id: string) {
    if (!this.canWrite(id)) return;
    this.write(id, { reset: [id] });
  }

  /** `e`'s field errors on the fields they name; when it names none of this Document's, on `id`. */
  private fieldErrors(e: ConfigurationError, id: string, fields: Record<string, FieldState>) {
    let named = false;
    for (const fe of e.fieldErrors) {
      if (!this.state.doc?.groups.some((g) => g.fields.some((f) => f.id === fe.id))) continue;
      named = true;
      fields = this.field(fe.id, { ...fields[fe.id], error: fe.message ? { code: fe.code, message: fe.message } : { code: fe.code } }, fields);
    }
    return named ? fields : this.field(id, { ...fields[id], error: problem(e) }, fields);
  }

  private write(id: string, change: { changes?: Record<string, FieldValue>; reset?: string[] }, pending?: FieldValue) {
    if (this.state.confirmation) return;
    const doc = this.state.doc!;
    const update: ConfigurationUpdate = { schema_version: CONFIGURATION_VERSION, id: doc.id, revision: doc.revision, ...change };
    // what the writer would refuse is said here, without asking
    const refused = checkUpdate(doc, update);
    if (refused) {
      this.set({ fields: this.fieldErrors(refused, id, this.state.fields) });
      return;
    }
    const field = this.known(id)!;
    if (this.guidance?.fields?.[id]?.rules?.length && change.changes && !doc.notices?.[id]
      && sameValue(change.changes[id], fieldValue(doc, field))) {
      this.discard(id);
      return;
    }
    const decision = this.guidanceFor(id, update);
    if (!decision) {
      this.set({ fields: this.field(id, { ...this.state.fields[id], saved: undefined, error: { code: "invalid_guidance" } }) });
    } else if (decision.confirm) {
      this.set({ confirmation: { fieldId: id, copy: decision.confirm, update, pending } });
    } else {
      void this.commit(id, update, pending, decision.afterSave);
    }
  }

  private guidanceFor(id: string, update: ConfigurationUpdate) {
    const rules = this.guidance?.fields?.[id]?.rules;
    if (!rules?.length) return {};
    const field = this.known(id)!;
    return changeGuidance(rules, update.reset ? "reset" : "change", fieldValue(this.state.doc!, field), update.changes?.[id] ?? field.default);
  }

  cancel(request: PendingConfirmation) {
    if (this.state.confirmation === request) this.set({ confirmation: null });
  }

  confirm(request: PendingConfirmation) {
    if (this.state.confirmation !== request || this.disposed) return;
    const { fieldId, update, pending } = request;
    if (this.canWrite(fieldId) && !checkUpdate(this.state.doc!, update)) {
      const decision = this.guidanceFor(fieldId, update);
      if (decision?.confirm === request.copy) {
        void this.commit(fieldId, update, pending, decision.afterSave);
        return;
      }
    }
    this.cancel(request);
  }

  private async commit(id: string, update: ConfigurationUpdate, pending?: FieldValue, afterSave?: string) {
    // from here to the end of the write, reads requested before either edge are stale: a write may answer with an
    // older document, and a read may answer with one from before the write committed or failed
    this.writeSeq += 1;
    const before = this.state.fields[id] ?? {};
    const guidance = this.guidance;
    const target = afterSave && (update.changes?.[id] ?? this.known(id)?.default);
    const sent = { draft: before.draft, selection: before.selection };
    this.set({ busy: id, confirmation: null, formError: null, fields: this.field(id, { ...before, error: undefined, saved: undefined, pending }) });
    try {
      const raw = await this.source.update(update, this.abort.signal);
      if (this.disposed) return;
      const parsed = parseDocument(raw);
      if (parsed.doc.id !== update.id) throw new ConfigurationError("invalid_document");
      this.apply(parsed.doc, parsed.writable);
      // what was sent is committed: its draft goes, unless it was edited again while on the way (that stays a draft)
      const now = this.state.fields[id] ?? {};
      const draft = now.draft !== undefined && now.draft !== sent.draft ? now.draft : undefined;
      const selection = now.selection && !sameList(now.selection, sent.selection) ? now.selection : undefined;
      const field = afterSave && this.known(id);
      const matchesTarget = field && parsed.writable && sameValue(fieldValue(parsed.doc, field), target);
      const receipt = afterSave && matchesTarget && guidance === this.guidance ? afterSave : true;
      this.set({ busy: null, blocked: null, fields: this.field(id, { draft, selection, saved: draft === undefined && selection === undefined ? receipt : undefined }) });
    } catch (e) {
      if (this.disposed || this.abortedByUs(e)) return;
      // the latest draft stays, edited during the write or not; the pending choice goes
      const now = this.state.fields[id] ?? {};
      const fields = this.field(id, { draft: now.draft, selection: now.selection });
      // Only a code the source names is a refusal this form can retry. A request that threw or timed out, or a
      // successful answer that isn't a Document, may have committed: only an explicit reload can tell.
      if (!(e instanceof ConfigurationError) || e.code === "invalid_document" || e.code === "failed") {
        this.set({ busy: null, blocked: { code: "unconfirmed" }, fields });
      } else if (this.guidance && e.code === "configuration_forbidden") {
        this.again = true;
        this.set({ busy: null, blocked: problem(e), fields });
      } else if (RELOAD_CODES.includes(e.code)) {
        this.set({ busy: null, blocked: problem(e), fields });
      } else if (e.code === "configuration_version") {
        this.set({ busy: null, writable: false, formError: problem(e), fields });
      } else {
        this.set({ busy: null, fields: this.fieldErrors(e, id, fields) });
      }
    } finally {
      // the write is over: a read requested during it is stale too, and the deferred one below starts after this edge
      this.writeSeq += 1;
      if (this.again && !this.disposed) {
        this.again = false;
        void this.read(false);
      }
    }
  }
}
