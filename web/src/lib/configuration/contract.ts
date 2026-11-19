// The portable settings form contract (#9877; schema/configuration/README.md, the server's `configuration` package,
// docs/web/web.md "Configuration forms"): a module describes one resource's settings as a Document (titled groups of
// typed fields with their effective values) and takes an Update that names only what changes. The same JSON is read by
// every client; schema/configuration holds its schema, example and shared validation cases.
//
// This file knows no module, user, assistant, hub or storage: a ConfigurationSource injected by the owning module reads
// and updates its one resource. Validation here mirrors the writer's (codes and order included) so a form can say what
// is wrong before it asks; the writer still checks everything itself, inside its commit.

export const CONFIGURATION_VERSION = 1;

export interface ConfigurationDetail {
  title: string;
  description: string;
}

export interface ConfigurationNotice {
  kind: "info" | "warning";
  message: string;
}

export interface ConfigurationOption {
  value: string;
  title: string;
  description?: string;
}

export interface TextOptions {
  multiline?: boolean;
  placeholder?: string;
  /** Unicode scalar values, not UTF-16 units. */
  min_length?: number;
  max_length?: number;
}

export interface SelectOptions {
  options: ConfigurationOption[];
  presentation?: "list" | "dropdown" | "segmented";
}

export interface MultiselectOptions {
  options: ConfigurationOption[];
  min_selected?: number;
  max_selected?: number;
}

export interface NumberOptions {
  integer?: boolean;
  minimum?: number;
  maximum?: number;
  /** A positive increment for the UI, not a constraint. */
  step?: number;
}

interface FieldBase {
  id: string;
  title: string;
  description?: string;
  read_only?: boolean;
  /** Only with read_only: why it can't be changed. */
  disabled_reason?: string;
  details?: ConfigurationDetail[];
}

export type ToggleField = FieldBase & { kind: "toggle"; default: boolean };
export type TextField = FieldBase & { kind: "text"; default: string; text: TextOptions };
export type SelectField = FieldBase & { kind: "select"; default: string; select: SelectOptions };
export type MultiselectField = FieldBase & { kind: "multiselect"; default: string[]; multiselect: MultiselectOptions };
export type NumberField = FieldBase & { kind: "number"; default: number; number: NumberOptions };
export type KnownField = ToggleField | TextField | SelectField | MultiselectField | NumberField;
/** A field this client cannot draw or check (a newer kind, or a definition that isn't valid): shown, never changed;
 *  its value stays as the source keeps it. */
export type UnsupportedField = FieldBase & { kind: "unsupported"; reason: "kind" | "definition" };
export type ConfigurationField = KnownField | UnsupportedField;

export type FieldValue = boolean | string | string[] | number;

export interface ConfigurationGroup {
  id: string;
  title: string;
  description?: string;
  fields: ConfigurationField[];
}

/** A complete snapshot of one resource. `values` holds one effective value per field, defaults included. */
export interface ConfigurationDocument {
  schema_version: number;
  id: string;
  /** Opaque; changes whenever values, permissions or definitions change. */
  revision: string;
  title: string;
  description?: string;
  groups: ConfigurationGroup[];
  values: Record<string, unknown>;
  notices?: Record<string, ConfigurationNotice>;
}

/** Changes only the fields it names; `reset` removes their overrides. Atomic, against `revision`. */
export interface ConfigurationUpdate {
  schema_version: number;
  id: string;
  revision: string;
  changes?: Record<string, unknown>;
  reset?: string[];
}

export interface ConfigurationFieldError {
  id: string;
  code: string;
  message?: string;
}

/** The writer's root error codes (README "Injected transport and authority"), and the HTTP status of each. */
export const ROOT_CODES = {
  configuration_bad_request: 400,
  configuration_version: 400,
  configuration_too_large: 413,
  configuration_invalid: 422,
  configuration_conflict: 409,
  configuration_forbidden: 403,
  configuration_not_found: 404,
  configuration_unavailable: 503,
} as const;

/** The writer's field codes, in the words of README "Injected transport and authority". */
export const FIELD_CODES = ["type", "option", "duplicate", "min_length", "max_length", "min_selected", "max_selected", "integer", "minimum", "maximum", "range", "unknown", "read_only", "unsupported"] as const;

/** Codes this client says in its own words: the writer's, and its own three (an answer that isn't a Document; a failure
 *  with no code; a write whose outcome couldn't be confirmed). Any other code shows the writer's message, or a generic
 *  line. */
export const ERROR_CODES: readonly string[] = [...Object.keys(ROOT_CODES), ...FIELD_CODES, "invalid_document", "failed", "unconfirmed"];

/** Codes after which the form reads again before it writes again: the resource changed, or a write's outcome is unknown. */
export const RELOAD_CODES: readonly string[] = ["configuration_conflict", "configuration_unavailable"];

/** What a source rejects with: the API's `{ error, code, field_errors }`. A conflict carries no replacement values. */
export class ConfigurationError extends Error {
  constructor(
    readonly code: string,
    message = "",
    readonly fieldErrors: ConfigurationFieldError[] = [],
  ) {
    super(message);
    this.name = "ConfigurationError";
  }

  /** From an `{ error, code, field_errors }` body; without a code, the root code its HTTP status stands for (any other
   *  server error is configuration_unavailable: the request's outcome is unknown). */
  static fromBody(body: unknown, status?: number): ConfigurationError {
    const b = isObject(body) ? body : {};
    const byStatus = Object.entries(ROOT_CODES).find(([code, s]) => s === status && code !== "configuration_version")?.[0];
    const fallback = byStatus ?? (status !== undefined && status >= 500 ? "configuration_unavailable" : "failed");
    const code = typeof b.code === "string" && b.code ? b.code : fallback;
    const message = typeof b.error === "string" ? b.error : "";
    const list = Array.isArray(b.field_errors) ? b.field_errors : [];
    const fields = list.flatMap((f): ConfigurationFieldError[] =>
      isObject(f) && typeof f.id === "string" && typeof f.code === "string" ? [{ id: f.id, code: f.code, ...(typeof f.message === "string" && f.message ? { message: f.message } : {}) }] : [],
    );
    return new ConfigurationError(code, message, fields);
  }
}

/**
 * One resource's settings, as its owning module provides them. `key` names the bound resource: a different key is a
 * different resource (the form starts over), the same key the same one. `subscribe`, when the module can tell, calls back
 * when the resource may have changed elsewhere; the form reads it again.
 */
export interface ConfigurationSource {
  readonly key: string;
  read(signal?: AbortSignal): Promise<unknown>;
  update(update: ConfigurationUpdate, signal?: AbortSignal): Promise<unknown>;
  subscribe?(onChange: () => void): () => void;
}

// ── checking ─────────────────────────────────────────────

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

const str = (v: unknown): v is string => typeof v === "string";
/** An id, revision, title or option value: some character that isn't whitespace (bytes kept as they are). */
const named = (v: unknown): v is string => str(v) && v.trim() !== "";
const optStr = (v: unknown) => v === undefined || str(v);
const optBool = (v: unknown) => v === undefined || typeof v === "boolean";
/** A count bound (a length, a number of choices): a non-negative safe integer, or absent. */
const optCount = (v: unknown) => v === undefined || (Number.isSafeInteger(v) && (v as number) >= 0);
/** A finite number inside the portable range (±2^53 − 1), decimals allowed. */
export const safeNumber = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v) && Math.abs(v) <= Number.MAX_SAFE_INTEGER;
const optSafe = (v: unknown) => v === undefined || safeNumber(v);
const ordered = (min: unknown, max: unknown) => min === undefined || max === undefined || (min as number) <= (max as number);

/** Length in Unicode scalar values (what the writer counts), not UTF-16 units. */
export const scalarLength = (s: string) => [...s].length;

const MEMBERS = ["text", "select", "multiselect", "number"] as const;

function details(v: unknown): ConfigurationDetail[] | undefined | null {
  if (v === undefined) return undefined;
  if (!Array.isArray(v)) return null;
  return v.every((d) => isObject(d) && named(d.title) && named(d.description)) ? (v as ConfigurationDetail[]) : null;
}

function options(v: unknown): ConfigurationOption[] | null {
  if (!Array.isArray(v) || !v.length) return null;
  if (!v.every((o) => isObject(o) && named(o.value) && named(o.title) && optStr(o.description))) return null;
  const values = v.map((o) => (o as ConfigurationOption).value);
  return new Set(values).size === values.length ? (v as ConfigurationOption[]) : null;
}

/** A field as this client can use it: a known kind with a valid definition and default, or unsupported (kept, shown). */
export function parseField(raw: unknown): ConfigurationField | null {
  if (!isObject(raw) || !named(raw.id) || !str(raw.kind) || !named(raw.title)) return null;
  const d = details(raw.details);
  const base: FieldBase = {
    id: raw.id,
    title: raw.title,
    ...(str(raw.description) && raw.description ? { description: raw.description } : {}),
    ...(raw.read_only === true ? { read_only: true } : {}),
    ...(str(raw.disabled_reason) && raw.disabled_reason ? { disabled_reason: raw.disabled_reason } : {}),
    ...(d ? { details: d } : {}),
  };
  const unsupported = (reason: "kind" | "definition"): UnsupportedField => ({ ...base, kind: "unsupported", reason });
  // a newer kind: shown as it is, whatever members it has
  if (raw.kind !== "toggle" && !(MEMBERS as readonly string[]).includes(raw.kind)) return unsupported("kind");
  if (d === null || !optStr(raw.description) || !optStr(raw.disabled_reason) || !optBool(raw.read_only)) return unsupported("definition");
  if (raw.disabled_reason && raw.read_only !== true) return unsupported("definition");
  // exactly the options member its kind names (a toggle has none)
  const present = MEMBERS.filter((m) => raw[m] !== undefined);
  if (raw.kind === "toggle" ? present.length !== 0 : present.length !== 1 || present[0] !== raw.kind) return unsupported("definition");
  let field: KnownField;
  switch (raw.kind) {
    case "toggle":
      field = { ...base, kind: "toggle", default: raw.default as boolean };
      break;
    case "text": {
      const o = raw.text;
      if (!isObject(o) || !optBool(o.multiline) || !optStr(o.placeholder) || !optCount(o.min_length) || !optCount(o.max_length) || !ordered(o.min_length, o.max_length)) return unsupported("definition");
      field = { ...base, kind: "text", default: raw.default as string, text: { ...(o.multiline === true ? { multiline: true } : {}), ...(o.placeholder ? { placeholder: o.placeholder as string } : {}), ...(o.min_length !== undefined ? { min_length: o.min_length as number } : {}), ...(o.max_length !== undefined ? { max_length: o.max_length as number } : {}) } };
      break;
    }
    case "select": {
      const o = raw.select;
      const list = isObject(o) ? options(o.options) : null;
      if (!isObject(o) || !list || !(o.presentation === undefined || o.presentation === "" || o.presentation === "list" || o.presentation === "dropdown" || o.presentation === "segmented")) return unsupported("definition");
      field = { ...base, kind: "select", default: raw.default as string, select: { options: list, ...(o.presentation ? { presentation: o.presentation as SelectOptions["presentation"] } : {}) } };
      break;
    }
    case "multiselect": {
      const o = raw.multiselect;
      const list = isObject(o) ? options(o.options) : null;
      if (!isObject(o) || !list || !optCount(o.min_selected) || !optCount(o.max_selected) || !ordered(o.min_selected, o.max_selected)) return unsupported("definition");
      if (o.min_selected !== undefined && (o.min_selected as number) > list.length) return unsupported("definition");
      field = { ...base, kind: "multiselect", default: raw.default as string[], multiselect: { options: list, ...(o.min_selected !== undefined ? { min_selected: o.min_selected as number } : {}), ...(o.max_selected !== undefined ? { max_selected: o.max_selected as number } : {}) } };
      break;
    }
    case "number": {
      const o = raw.number;
      if (!isObject(o) || !optBool(o.integer) || !optSafe(o.minimum) || !optSafe(o.maximum) || !optSafe(o.step) || !ordered(o.minimum, o.maximum)) return unsupported("definition");
      if (o.step !== undefined && !((o.step as number) > 0)) return unsupported("definition");
      field = { ...base, kind: "number", default: raw.default as number, number: { ...(o.integer === true ? { integer: true } : {}), ...(o.minimum !== undefined ? { minimum: o.minimum as number } : {}), ...(o.maximum !== undefined ? { maximum: o.maximum as number } : {}), ...(o.step !== undefined ? { step: o.step as number } : {}) } };
      break;
    }
    default:
      return unsupported("kind");
  }
  return checkValue(field, field.default) ? unsupported("definition") : field;
}

/** Why `value` is not a value of `field` (the writer's field code), or null when it is one. */
export function checkValue(field: KnownField, value: unknown): string | null {
  switch (field.kind) {
    case "toggle":
      return typeof value === "boolean" ? null : "type";
    case "text": {
      if (!str(value)) return "type";
      const n = scalarLength(value);
      if (field.text.min_length !== undefined && n < field.text.min_length) return "min_length";
      if (field.text.max_length !== undefined && n > field.text.max_length) return "max_length";
      return null;
    }
    case "select":
      return !str(value) ? "type" : field.select.options.some((o) => o.value === value) ? null : "option";
    case "multiselect": {
      if (!Array.isArray(value)) return "type";
      const seen = new Set<string>();
      // each choice in turn, as the writer checks them
      for (const v of value) {
        if (!str(v)) return "type";
        if (!field.multiselect.options.some((o) => o.value === v)) return "option";
        if (seen.has(v)) return "duplicate";
        seen.add(v);
      }
      if (field.multiselect.min_selected !== undefined && value.length < field.multiselect.min_selected) return "min_selected";
      if (field.multiselect.max_selected !== undefined && value.length > field.multiselect.max_selected) return "max_selected";
      return null;
    }
    case "number": {
      if (typeof value !== "number") return "type";
      if (!safeNumber(value)) return "range";
      if (field.number.integer && !Number.isInteger(value)) return "integer";
      if (field.number.minimum !== undefined && value < field.number.minimum) return "minimum";
      if (field.number.maximum !== undefined && value > field.number.maximum) return "maximum";
      return null;
    }
  }
}

/** A Document as this client reads it. `writable` is false for a schema version it doesn't know (shown, never
 *  written). Throws ConfigurationError("invalid_document") for something that isn't a Document at all. */
export interface ParsedDocument {
  doc: ConfigurationDocument;
  writable: boolean;
}

export function parseDocument(raw: unknown): ParsedDocument {
  const bad = () => new ConfigurationError("invalid_document");
  if (!isObject(raw) || !Number.isSafeInteger(raw.schema_version) || !named(raw.id) || !named(raw.revision) || !named(raw.title)) throw bad();
  const writable = raw.schema_version === CONFIGURATION_VERSION;
  const values = isObject(raw.values) ? raw.values : null;
  const groupsRaw = Array.isArray(raw.groups) ? raw.groups : null;
  if (writable && (!values || !groupsRaw)) throw bad();
  const groups: ConfigurationGroup[] = [];
  const groupIds = new Set<string>();
  const ids = new Set<string>();
  for (const g of groupsRaw ?? []) {
    if (!isObject(g) || !named(g.id) || !named(g.title) || !Array.isArray(g.fields) || groupIds.has(g.id)) {
      if (writable) throw bad();
      continue;
    }
    groupIds.add(g.id);
    const fields: ConfigurationField[] = [];
    for (const f of g.fields) {
      const field = parseField(f);
      if (!field || ids.has(field.id)) {
        if (writable) throw bad();
        continue;
      }
      ids.add(field.id);
      fields.push(field);
    }
    groups.push({ id: g.id, title: g.title, ...(str(g.description) && g.description ? { description: g.description } : {}), fields });
  }
  const notices: Record<string, ConfigurationNotice> = {};
  if (isObject(raw.notices)) {
    for (const [id, n] of Object.entries(raw.notices)) if (isObject(n) && (n.kind === "info" || n.kind === "warning") && named(n.message)) notices[id] = { kind: n.kind, message: n.message };
  }
  return {
    doc: { schema_version: raw.schema_version as number, id: raw.id, revision: raw.revision, title: raw.title, ...(str(raw.description) && raw.description ? { description: raw.description } : {}), groups, values: values ?? {}, notices },
    writable,
  };
}

function fieldsOf(doc: ConfigurationDocument): Map<string, ConfigurationField> {
  return new Map(doc.groups.flatMap((g) => g.fields.map((f) => [f.id, f] as const)));
}

/**
 * What the writer would say to `update` against `doc` (README "Change and reset", the server's CheckUpdate): null when
 * it would accept it, else the error. A version it doesn't know, then another resource or revision, then an empty update,
 * then each change (sorted by id) and each reset: unknown, unsupported, read-only or invalid fields, duplicate resets, a
 * field both changed and reset. A field this client can't interpret also rejects either operation locally.
 */
export function checkUpdate(doc: ConfigurationDocument, update: ConfigurationUpdate): ConfigurationError | null {
  if (update.schema_version !== CONFIGURATION_VERSION) return new ConfigurationError("configuration_version");
  if (update.id !== doc.id || update.revision !== doc.revision) return new ConfigurationError("configuration_conflict");
  const changes = isObject(update.changes) ? update.changes : {};
  const reset = Array.isArray(update.reset) ? update.reset : [];
  if (!Object.keys(changes).length && !reset.length) return new ConfigurationError("configuration_invalid");
  const fields = fieldsOf(doc);
  const problems: ConfigurationFieldError[] = [];
  const check = (id: string, value: unknown, isReset: boolean) => {
    const f = fields.get(id);
    const code = !f ? "unknown" : f.kind === "unsupported" ? "unsupported" : f.read_only ? "read_only" : isReset ? null : checkValue(f, value);
    if (code) problems.push({ id, code });
  };
  for (const id of Object.keys(changes).sort()) check(id, changes[id], false);
  const seen = new Set<string>();
  for (const id of reset) {
    if (Object.hasOwn(changes, id) || seen.has(id)) problems.push({ id, code: "duplicate" });
    else check(id, undefined, true);
    seen.add(id);
  }
  return problems.length ? new ConfigurationError("configuration_invalid", "", problems) : null;
}

/** The field's current value when it is a valid one; undefined when the document holds none or an invalid one. */
export function fieldValue(doc: ConfigurationDocument, field: KnownField): FieldValue | undefined {
  const v = doc.values[field.id];
  return checkValue(field, v) ? undefined : (v as FieldValue);
}

/** Whether two values of a field are the same (a multiselect's choices in any order: membership is what counts). */
export function sameValue(a: unknown, b: unknown): boolean {
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((v) => b.includes(v));
  return Object.is(a, b);
}
