// A configuration form's state over an injected source (controller.ts) and the HTTP source (http.ts): nothing saved
// before the source commits, drafts that survive failures and conflicts, typed-while-saving text kept on either answer,
// a multiselect's checks as one pending replacement, late answers from a replaced source or an older read ignored,
// partial updates and resets, field errors, versions it can't write, and changes made elsewhere. The document is the
// shared synthetic one (schema/configuration/all-types.json).
import { describe, expect, it, vi } from "vitest";
import shared from "../../../../schema/configuration/all-types.json";
import { ConfigurationController } from "./controller";
import { ConfigurationError, type ConfigurationSource, type ConfigurationUpdate } from "./contract";
import { httpConfigurationSource, type ConfigurationRequest } from "./http";

interface Field {
  id: string;
  kind: string;
  default: unknown;
  read_only?: boolean;
  [member: string]: unknown;
}
interface Doc {
  schema_version: number;
  id: string;
  revision: string;
  title: string;
  groups: { id: string; title: string; fields: Field[] }[];
  values: Record<string, unknown>;
  notices?: Record<string, unknown>;
}
const example = shared as unknown as Doc;
const clone = (d: Doc): Doc => JSON.parse(JSON.stringify(d));

/** A source whose answers the test releases one by one. */
function manual(key = "example-preferences") {
  const reads: { resolve: (v: unknown) => void; reject: (e: unknown) => void }[] = [];
  const updates: { body: ConfigurationUpdate; resolve: (v: unknown) => void; reject: (e: unknown) => void }[] = [];
  let changed: (() => void) | null = null;
  const source: ConfigurationSource = {
    key,
    read: () => new Promise((resolve, reject) => reads.push({ resolve, reject })),
    update: (body) => new Promise((resolve, reject) => updates.push({ body, resolve, reject })),
    subscribe: (l) => {
      changed = l;
      return () => {
        changed = null;
      };
    },
  };
  return { source, reads, updates, change: () => changed?.(), get subscribed() { return changed !== null; } };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

/** Commits what an update asks on `doc` (the way a writer would), as a new revision. */
function commit(doc: Doc, u: ConfigurationUpdate): Doc {
  const next = clone(doc);
  const fields = next.groups.flatMap((g) => g.fields);
  Object.assign(next.values, u.changes ?? {});
  for (const id of u.reset ?? []) (next.values as Record<string, unknown>)[id] = fields.find((f) => f.id === id)!.default;
  next.revision = `${doc.revision}+`;
  return next;
}

async function ready(m = manual(), doc: Doc = example) {
  const ctl = new ConfigurationController(m.source);
  ctl.start();
  m.reads[0].resolve(doc);
  await flush();
  return { ctl, m };
}

/** The shared document with `sections` fixed to exactly one choice. */
function oneOfOne(): Doc {
  const doc = clone(example);
  for (const f of doc.groups.flatMap((g) => g.fields)) if (f.id === "sections") f.multiselect = { min_selected: 1, max_selected: 1, options: (f.multiselect as { options: unknown[] }).options };
  return doc;
}

describe("reading", () => {
  it("loads, then is ready with the document", async () => {
    const m = manual();
    const ctl = new ConfigurationController(m.source);
    ctl.start();
    expect(ctl.snapshot().phase).toBe("loading");
    m.reads[0].resolve(example);
    await flush();
    expect(ctl.snapshot()).toMatchObject({ phase: "ready", writable: true, readError: null });
    expect(ctl.snapshot().doc?.revision).toBe("revision-1");
  });

  it("says why it couldn't read, and reads again on Retry", async () => {
    const m = manual();
    const ctl = new ConfigurationController(m.source);
    ctl.start();
    m.reads[0].reject(new ConfigurationError("configuration_forbidden", "Not yours"));
    await flush();
    expect(ctl.snapshot()).toMatchObject({ phase: "failed", readError: { code: "configuration_forbidden", message: "Not yours" } });
    ctl.reload();
    expect(ctl.snapshot().phase).toBe("loading");
    m.reads[1].resolve(example);
    await flush();
    expect(ctl.snapshot().phase).toBe("ready");
  });

  it("refuses something that isn't a document", async () => {
    const { ctl } = await ready(manual(), { hello: 1 } as unknown as Doc);
    expect(ctl.snapshot()).toMatchObject({ phase: "failed", readError: { code: "invalid_document" } });
  });

  it("a disposed form (its source replaced) ignores a late answer", async () => {
    const m = manual();
    const ctl = new ConfigurationController(m.source);
    ctl.start();
    const seen = vi.fn();
    ctl.subscribe(seen);
    ctl.dispose();
    expect(m.subscribed).toBe(false);
    m.reads[0].resolve(example);
    await flush();
    expect(ctl.snapshot().phase).toBe("loading");
    expect(seen).not.toHaveBeenCalled();
  });

  it("an older read that answers after a newer one is ignored", async () => {
    const { ctl, m } = await ready();
    m.change(); // read #2
    ctl.reload(); // read #3
    m.reads[2].resolve({ ...clone(example), revision: "revision-3" });
    await flush();
    m.reads[1].resolve({ ...clone(example), revision: "revision-2-old" });
    await flush();
    expect(ctl.snapshot().doc?.revision).toBe("revision-3");
  });

  it("keeps the document and says so when reading again fails", async () => {
    const { ctl, m } = await ready();
    m.change();
    m.reads[1].reject(new Error("offline"));
    await flush();
    expect(ctl.snapshot()).toMatchObject({ phase: "ready", readError: { code: "failed" } });
    expect(ctl.snapshot().doc?.revision).toBe("revision-1");
  });
});

describe("writing a choice", () => {
  it("sends only that field against the revision, shows it pending, and saved only once committed", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense");
    expect(m.updates[0].body).toEqual({ schema_version: 1, id: "example-preferences", revision: "revision-1", changes: { layout: "dense" } });
    expect(ctl.snapshot().busy).toBe("layout");
    expect(ctl.snapshot().fields.layout).toEqual({ pending: "dense" });
    expect(ctl.snapshot().doc?.values.layout).toBe("comfortable"); // nothing changed before the source said so
    m.updates[0].resolve(commit(example, m.updates[0].body));
    await flush();
    expect(ctl.snapshot().busy).toBeNull();
    expect(ctl.snapshot().fields.layout).toEqual({ saved: true });
    expect(ctl.snapshot().doc?.values.layout).toBe("dense");
    expect(ctl.snapshot().doc?.revision).toBe("revision-1+");
  });

  it("puts back what the document says when the write fails, and says why", async () => {
    const { ctl, m } = await ready();
    ctl.choose("compact", true);
    m.updates[0].reject(new ConfigurationError("storage_failed", "Couldn't keep it"));
    await flush();
    expect(ctl.snapshot().fields.compact).toEqual({ error: { code: "storage_failed", message: "Couldn't keep it" } });
    expect(ctl.snapshot().doc?.values.compact).toBe(false);
  });

  it("writes one thing at a time", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense");
    ctl.choose("accent", "green");
    expect(m.updates).toHaveLength(1);
    expect(ctl.canWrite("accent")).toBe(false);
  });

  it("checks a choice before sending it (unknown choices, counts)", async () => {
    const { ctl, m } = await ready();
    ctl.choose("sections", ["summary", "summary"]);
    expect(ctl.snapshot().fields.sections.error).toEqual({ code: "duplicate" });
    ctl.choose("sections", []);
    expect(ctl.snapshot().fields.sections.error).toEqual({ code: "min_selected" });
    ctl.choose("layout", "future");
    expect(ctl.snapshot().fields.layout.error).toEqual({ code: "option" });
    expect(m.updates).toHaveLength(0);
  });

  it("never writes a read-only or disabled field", async () => {
    const { ctl, m } = await ready();
    ctl.choose("managed", false);
    expect(m.updates).toHaveLength(0);
    expect(ctl.editable("managed")).toBe(false);
  });

  it("never writes a document of a version it doesn't know", async () => {
    const { ctl, m } = await ready(manual(), { ...clone(example), schema_version: 2 });
    expect(ctl.snapshot().writable).toBe(false);
    ctl.choose("layout", "dense");
    ctl.reset("layout");
    expect(m.updates).toHaveLength(0);
  });
});

describe("typed text", () => {
  it("is a draft until saved, checked first, and gone once committed", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "x".repeat(21));
    ctl.saveDraft("label");
    expect(ctl.snapshot().fields.label.error).toEqual({ code: "max_length" });
    expect(m.updates).toHaveLength(0);
    ctl.setDraft("label", "Sam 👋");
    expect(ctl.snapshot().fields.label).toEqual({ draft: "Sam 👋" });
    ctl.saveDraft("label");
    expect(m.updates[0].body.changes).toEqual({ label: "Sam 👋" });
    m.updates[0].resolve(commit(example, m.updates[0].body));
    await flush();
    expect(ctl.snapshot().fields.label).toEqual({ saved: true });
    expect(ctl.snapshot().doc?.values.label).toBe("Sam 👋");
  });

  it("keeps the draft through a failed save the source names", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("notes", "New words");
    ctl.saveDraft("notes");
    m.updates[0].reject(new ConfigurationError("storage_failed", "The disk is full"));
    await flush();
    expect(ctl.snapshot().fields.notes).toEqual({ draft: "New words", error: { code: "storage_failed", message: "The disk is full" } });
  });

  it("keeps text edited while its save is on the way, after the save succeeds", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "A");
    ctl.saveDraft("label");
    expect(m.updates[0].body.changes).toEqual({ label: "A" });
    ctl.setDraft("label", "AB"); // typed while the promise is pending
    expect(ctl.snapshot().fields.label).toEqual({ draft: "AB" });
    m.updates[0].resolve(commit(example, m.updates[0].body));
    await flush();
    expect(ctl.snapshot().doc?.values.label).toBe("A"); // what was sent is committed
    expect(ctl.snapshot().fields.label).toEqual({ draft: "AB" }); // the newer text stays, not saved
  });

  it("keeps text edited while its save is on the way, after the save fails", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("notes", "A");
    ctl.saveDraft("notes");
    ctl.setDraft("notes", "AB");
    m.updates[0].reject(new ConfigurationError("storage_failed", "Couldn't keep it"));
    await flush();
    expect(ctl.snapshot().fields.notes).toEqual({ draft: "AB", error: { code: "storage_failed", message: "Couldn't keep it" } });
  });

  it("keeps the draft through a conflict, reads again only when asked, and still has it after", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("notes", "Mine");
    ctl.saveDraft("notes");
    m.updates[0].reject(new ConfigurationError("configuration_conflict"));
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "configuration_conflict" });
    expect(m.reads).toHaveLength(1); // nothing re-read on its own
    expect(ctl.snapshot().fields.notes).toEqual({ draft: "Mine" });
    expect(ctl.canWrite("label")).toBe(false); // no writing until Reload
    ctl.setDraft("label", "Also mine");
    ctl.saveDraft("label");
    expect(m.updates).toHaveLength(1);
    ctl.reload();
    m.reads[1].resolve({ ...clone(example), revision: "revision-2", values: { ...example.values, notes: "Theirs" } });
    await flush();
    expect(ctl.snapshot().blocked).toBeNull();
    expect(ctl.snapshot().doc?.values.notes).toBe("Theirs");
    expect(ctl.snapshot().fields.notes).toEqual({ draft: "Mine" });
  });

  it("an uncertain write (unavailable) needs an explicit reload too, keeping the draft", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("notes", "Mine");
    ctl.saveDraft("notes");
    m.updates[0].reject(new ConfigurationError("configuration_unavailable"));
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "configuration_unavailable" });
    expect(m.reads).toHaveLength(1);
    expect(ctl.snapshot().fields.notes).toEqual({ draft: "Mine" });
    ctl.reload();
    m.reads[1].resolve(commit(example, { schema_version: 1, id: example.id, revision: example.revision, changes: { notes: "Mine" } }));
    await flush();
    expect(ctl.snapshot().blocked).toBeNull();
    expect(ctl.snapshot().doc?.values.notes).toBe("Mine"); // the lost write had landed
    expect(ctl.snapshot().fields.notes).toEqual({ draft: "Mine" });
  });

  it("an unknown version blocks writing altogether, keeping the draft", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("notes", "Mine");
    ctl.saveDraft("notes");
    m.updates[0].reject(new ConfigurationError("configuration_version"));
    await flush();
    expect(ctl.snapshot()).toMatchObject({ writable: false, formError: { code: "configuration_version" }, busy: null });
    expect(ctl.snapshot().fields.notes).toEqual({ draft: "Mine" });
    expect(ctl.canWrite("label")).toBe(false);
  });

  it("keeps a draft when the resource changes elsewhere", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "Typing");
    m.change();
    m.reads[1].resolve({ ...clone(example), revision: "revision-2" });
    await flush();
    expect(ctl.snapshot().doc?.revision).toBe("revision-2");
    expect(ctl.snapshot().fields.label).toEqual({ draft: "Typing" });
  });

  it("puts field errors on the fields they name", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "taken");
    ctl.saveDraft("label");
    m.updates[0].reject(new ConfigurationError("configuration_invalid", "", [{ id: "label", code: "taken", message: "Someone has that name" }]));
    await flush();
    expect(ctl.snapshot().fields.label).toEqual({ draft: "taken", error: { code: "taken", message: "Someone has that name" } });
  });

  it("numbers: parsed and checked as the field says; anything else stays a draft with why", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("interval", "2.5");
    ctl.saveDraft("interval");
    expect(ctl.snapshot().fields.interval.error).toEqual({ code: "integer" });
    ctl.setDraft("interval", "abc");
    ctl.saveDraft("interval");
    expect(ctl.snapshot().fields.interval.error).toEqual({ code: "type" });
    ctl.setDraft("interval", "");
    ctl.saveDraft("interval");
    expect(ctl.snapshot().fields.interval.error).toEqual({ code: "type" });
    ctl.setDraft("interval", "40");
    ctl.saveDraft("interval");
    expect(m.updates[0].body.changes).toEqual({ interval: 40 });
  });

  it("Discard drops the draft", async () => {
    const { ctl } = await ready();
    ctl.setDraft("label", "Oops");
    ctl.discard("label");
    expect(ctl.snapshot().fields.label).toBeUndefined();
  });
});

describe("a multiselect's pending selection", () => {
  it("changes locally one check at a time and sends the final list on Save", async () => {
    const { ctl, m } = await ready(manual(), oneOfOne());
    ctl.setSelection("sections", ["summary", "activity"]); // an invalid intermediate: one is checked before the other goes
    ctl.setSelection("sections", ["files"]);
    expect(m.updates).toHaveLength(0);
    expect(ctl.snapshot().fields.sections).toEqual({ selection: ["files"] });
    ctl.saveDraft("sections");
    expect(m.updates[0].body.changes).toEqual({ sections: ["files"] });
    m.updates[0].resolve(commit(oneOfOne(), m.updates[0].body));
    await flush();
    expect(ctl.snapshot().doc?.values.sections).toEqual(["files"]);
    expect(ctl.snapshot().fields.sections).toEqual({ saved: true });
  });

  it("checks the count on Save, sends nothing when it doesn't fit, and keeps the checks", async () => {
    const { ctl, m } = await ready(manual(), oneOfOne());
    ctl.setSelection("sections", []);
    ctl.saveDraft("sections");
    expect(m.updates).toHaveLength(0);
    expect(ctl.snapshot().fields.sections).toEqual({ selection: [], error: { code: "min_selected" } });
  });

  it("Discard drops the checks, back to what is saved", async () => {
    const { ctl } = await ready();
    ctl.setSelection("sections", ["activity"]);
    ctl.discard("sections");
    expect(ctl.snapshot().fields.sections).toBeUndefined();
  });

  it("keeps a selection changed while its save is on the way, after it succeeds", async () => {
    const { ctl, m } = await ready();
    ctl.setSelection("sections", ["summary", "activity"]);
    ctl.saveDraft("sections");
    ctl.setSelection("sections", ["summary", "files"]); // changed while pending
    m.updates[0].resolve(commit(example, m.updates[0].body));
    await flush();
    expect(ctl.snapshot().doc?.values.sections).toEqual(["summary", "activity"]);
    expect(ctl.snapshot().fields.sections).toEqual({ selection: ["summary", "files"] });
  });
});

describe("resetting", () => {
  it("sends reset for that field only", async () => {
    const { ctl, m } = await ready();
    ctl.reset("interval");
    expect(m.updates[0].body).toEqual({ schema_version: 1, id: "example-preferences", revision: "revision-1", reset: ["interval"] });
    m.updates[0].resolve(commit(example, m.updates[0].body));
    await flush();
    expect(ctl.snapshot().doc?.values.interval).toBe(30);
    expect(ctl.snapshot().doc?.values.layout).toBe("comfortable"); // the rest untouched
  });
});

describe("a write with no usable answer", () => {
  it("a transport exception blocks writes until Reload and keeps the latest draft", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "Mine");
    ctl.saveDraft("label");
    m.updates[0].reject(new TypeError("Failed to fetch"));
    await flush();
    expect(ctl.snapshot().busy).toBeNull();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
    expect(ctl.snapshot().fields.label).toEqual({ draft: "Mine" });
    expect(ctl.canWrite("label")).toBe(false);
    ctl.saveDraft("label");
    expect(m.updates).toHaveLength(1); // no second PATCH
    ctl.reload();
    m.reads[1].resolve(commit(example, { schema_version: 1, id: example.id, revision: example.revision, changes: { label: "Mine" } }));
    await flush();
    expect(ctl.snapshot().blocked).toBeNull();
    expect(ctl.snapshot().fields.label).toEqual({ draft: "Mine" });
    ctl.saveDraft("label");
    expect(m.updates).toHaveLength(2);
    expect(m.updates[1].body.changes).toEqual({ label: "Mine" });
  });

  it("a transport timeout clears busy (it isn't this controller's own abort) and blocks", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "Mine");
    ctl.saveDraft("label");
    m.updates[0].reject(new DOMException("timed out", "AbortError"));
    await flush();
    expect(ctl.snapshot().busy).toBeNull();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
    expect(ctl.snapshot().fields.label).toEqual({ draft: "Mine" });
    expect(m.reads).toHaveLength(1); // nothing re-read on its own
  });

  it("a successful answer that isn't a Document may have committed: it blocks too", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "Mine");
    ctl.saveDraft("label");
    m.updates[0].resolve(null);
    await flush();
    expect(ctl.snapshot().busy).toBeNull();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
    expect(ctl.snapshot().fields.label).toEqual({ draft: "Mine" });
    expect(m.updates).toHaveLength(1);
  });

  it("a code the source names is a refusal it can retry at once", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "Mine");
    ctl.saveDraft("label");
    m.updates[0].reject(new ConfigurationError("storage_failed", "Couldn't keep it"));
    await flush();
    expect(ctl.snapshot().blocked).toBeNull();
    expect(ctl.snapshot().fields.label).toEqual({ draft: "Mine", error: { code: "storage_failed", message: "Couldn't keep it" } });
    ctl.saveDraft("label");
    expect(m.updates).toHaveLength(2);
  });

  it("its own dispose still ignores the abort it caused", async () => {
    const { ctl, m } = await ready();
    ctl.setDraft("label", "Mine");
    ctl.saveDraft("label");
    const seen = vi.fn();
    ctl.subscribe(seen);
    ctl.dispose();
    m.updates[0].reject(new DOMException("aborted", "AbortError"));
    await flush();
    expect(seen).not.toHaveBeenCalled();
  });

  it("a read that times out is reported instead of ignored", async () => {
    const { ctl, m } = await ready();
    m.change();
    m.reads[1].reject(new DOMException("timed out", "AbortError"));
    await flush();
    expect(ctl.snapshot()).toMatchObject({ phase: "ready", readError: { code: "failed" } });
    expect(ctl.snapshot().doc?.revision).toBe("revision-1");
  });
});

describe("a change made elsewhere during a write", () => {
  it("reads again once the write is done", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense");
    m.change();
    expect(m.reads).toHaveLength(1);
    m.updates[0].resolve(commit(example, m.updates[0].body));
    await flush();
    expect(m.reads).toHaveLength(2);
    expect(ctl.snapshot().busy).toBeNull();
  });

  it("keeps the committed choice saved when the deferred read is its own same-revision echo", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense");
    m.change(); // a change notification while the write is in flight: the read waits for it
    const committed = commit(example, m.updates[0].body);
    m.updates[0].resolve(committed);
    await flush();
    expect(ctl.snapshot().busy).toBeNull();
    expect(ctl.snapshot().fields.layout).toEqual({ saved: true });
    // the read after the write returns the committed Document again (a source's echo of its own write)
    expect(m.reads).toHaveLength(2);
    m.reads[1].resolve(committed);
    await flush();
    expect(ctl.snapshot().fields.layout).toEqual({ saved: true });
    expect(ctl.snapshot().blocked).toBeNull();
    expect(ctl.snapshot().doc?.revision).toBe(committed.revision);
  });
});

describe("a read that raced a write", () => {
  it("does not let a pre-write explicit reload clear a conflict", async () => {
    const { ctl, m } = await ready();
    ctl.reload(); // explicit read #2 starts before the write
    ctl.choose("layout", "dense");
    m.updates[0].reject(new ConfigurationError("configuration_conflict"));
    await flush();
    expect(ctl.snapshot().blocked).toMatchObject({ code: "configuration_conflict" });
    m.reads[1].resolve(example); // the read that predates the write answers last
    await flush();
    expect(ctl.snapshot().blocked).toMatchObject({ code: "configuration_conflict" });
    expect(ctl.snapshot().doc?.revision).toBe("revision-1");
  });

  it("does not let a pre-write explicit reload clear an unknown outcome", async () => {
    const { ctl, m } = await ready();
    ctl.reload();
    ctl.choose("layout", "dense");
    m.updates[0].reject(new TypeError("Failed to fetch"));
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
    m.reads[1].resolve(example);
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
  });

  it("ignores a pre-write read's late failure after the write committed", async () => {
    const { ctl, m } = await ready();
    ctl.reload();
    ctl.choose("layout", "dense");
    const committed = commit(example, m.updates[0].body);
    m.updates[0].resolve(committed);
    await flush();
    m.reads[1].reject(new Error("offline"));
    await flush();
    expect(ctl.snapshot().readError).toBeNull();
    expect(ctl.snapshot().fields.layout).toEqual({ saved: true });
    expect(ctl.snapshot().doc?.revision).toBe(committed.revision);
  });

  it("never lets a read started before a successful write replace its result", async () => {
    const { ctl, m } = await ready();
    ctl.reload();
    ctl.choose("layout", "dense");
    const committed = commit(example, m.updates[0].body);
    m.updates[0].resolve(committed);
    await flush();
    m.reads[1].resolve(example); // the older revision
    await flush();
    expect(ctl.snapshot().doc?.revision).toBe(committed.revision);
    expect(ctl.snapshot().doc?.values.layout).toBe("dense");
    expect(ctl.snapshot().fields.layout).toEqual({ saved: true });
  });

  it("still applies the deferred read, which starts after the write", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense");
    m.change(); // a change while the write is in flight: read once it is done
    const committed = commit(example, m.updates[0].body);
    m.updates[0].resolve(committed);
    await flush();
    expect(m.reads).toHaveLength(2);
    const newer = { ...clone(committed), revision: "revision-2", values: { ...committed.values, layout: "comfortable" } };
    m.reads[1].resolve(newer);
    await flush();
    expect(ctl.snapshot().doc?.revision).toBe("revision-2");
    expect(ctl.snapshot().doc?.values.layout).toBe("comfortable");
  });

  it("lets a fresh explicit reload after the conflict clear the barrier", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense");
    m.updates[0].reject(new ConfigurationError("configuration_conflict"));
    await flush();
    expect(ctl.snapshot().blocked).toMatchObject({ code: "configuration_conflict" });
    ctl.reload();
    const newer = { ...clone(example), revision: "revision-2", values: { ...example.values, layout: "comfortable" } };
    m.reads[1].resolve(newer);
    await flush();
    expect(ctl.snapshot().blocked).toBeNull();
    expect(ctl.snapshot().doc?.revision).toBe("revision-2");
  });

  it("does not let a read requested during a write apply before the write answers", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense"); // the update stays unresolved
    ctl.reload(); // an explicit read while the write is in flight
    expect(ctl.snapshot().busy).toBe("layout");
    expect(ctl.snapshot().fields.layout).toEqual({ pending: "dense" });
    const newer = { ...clone(example), revision: "revision-2", values: { ...example.values, layout: "comfortable" } };
    m.reads[1].resolve(newer); // it answers before the write does
    await flush();
    // the pending choice and the Document stay until the write answers
    expect(ctl.snapshot().busy).toBe("layout");
    expect(ctl.snapshot().fields.layout).toEqual({ pending: "dense" });
    expect(ctl.snapshot().doc?.revision).toBe("revision-1");
    m.updates[0].resolve(commit(example, m.updates[0].body));
    await flush();
    expect(ctl.snapshot().busy).toBeNull();
    expect(ctl.snapshot().fields.layout).toEqual({ saved: true });
    expect(ctl.snapshot().doc?.revision).toBe("revision-1+");
  });

  it("ignores a read requested during a write that answers after a conflict", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense");
    ctl.reload();
    m.updates[0].reject(new ConfigurationError("configuration_conflict"));
    await flush();
    expect(ctl.snapshot().blocked).toMatchObject({ code: "configuration_conflict" });
    m.reads[1].resolve({ ...clone(example), revision: "revision-2", values: { ...example.values, layout: "comfortable" } });
    await flush();
    expect(ctl.snapshot().blocked).toMatchObject({ code: "configuration_conflict" });
    expect(ctl.snapshot().doc?.revision).toBe("revision-1");
  });

  it("ignores a read requested during a write that answers after an unknown outcome", async () => {
    const { ctl, m } = await ready();
    ctl.choose("layout", "dense");
    ctl.reload();
    m.updates[0].reject(new TypeError("Failed to fetch"));
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
    m.reads[1].resolve({ ...clone(example), revision: "revision-2", values: { ...example.values, layout: "comfortable" } });
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
    expect(ctl.snapshot().doc?.revision).toBe("revision-1");
  });
});

describe("the HTTP source", () => {
  function http(answer: (r: ConfigurationRequest) => { status: number; body: unknown }) {
    const calls: [string, ConfigurationRequest][] = [];
    const source = httpConfigurationSource({
      endpoint: "/api/v1/example/settings",
      transport: async (endpoint, request) => {
        calls.push([endpoint, request]);
        return answer(request);
      },
    });
    return { source, calls };
  }

  it("GETs the endpoint for the document and PATCHes it with the update; its key is the endpoint", async () => {
    const { source, calls } = http((r) => ({ status: 200, body: r.method === "GET" ? example : commit(example, JSON.parse(r.body!)) }));
    expect(source.key).toBe("/api/v1/example/settings");
    await expect(source.read()).resolves.toEqual(example);
    const update = { schema_version: 1, id: "example-preferences", revision: "revision-1", changes: { layout: "dense" } };
    const doc = (await source.update(update)) as Doc;
    expect(doc.values.layout).toBe("dense");
    expect(calls.map(([e, r]) => [e, r.method, r.body ? JSON.parse(r.body) : undefined])).toEqual([
      ["/api/v1/example/settings", "GET", undefined],
      ["/api/v1/example/settings", "PATCH", update],
    ]);
  });

  it("turns an error answer into a ConfigurationError (409 is a conflict, 503 unavailable, even without a code)", async () => {
    const { source } = http((r) => (r.method === "GET" ? { status: 403, body: { error: "No", code: "configuration_forbidden" } } : { status: 409, body: undefined }));
    await expect(source.read()).rejects.toMatchObject({ code: "configuration_forbidden", message: "No" });
    await expect(source.update({ schema_version: 1, id: "example-preferences", revision: "revision-1" })).rejects.toMatchObject({ code: "configuration_conflict" });
    const unavailable = http(() => ({ status: 503, body: undefined })).source;
    await expect(unavailable.update({ schema_version: 1, id: "example-preferences", revision: "revision-1" })).rejects.toMatchObject({ code: "configuration_unavailable" });
  });

  it("a 502 with no body is an unavailable outcome: a second PATCH waits for Reload", async () => {
    let gateway = true;
    const calls: ConfigurationRequest[] = [];
    const source = httpConfigurationSource({
      endpoint: "/api/v1/example/settings",
      transport: async (_endpoint, request) => {
        calls.push(request);
        if (request.method === "GET") return { status: 200, body: example };
        return gateway ? { status: 502, body: undefined } : { status: 200, body: commit(example, JSON.parse(request.body!)) };
      },
    });
    const ctl = new ConfigurationController(source);
    ctl.start();
    await flush();
    ctl.setDraft("label", "Mine");
    ctl.saveDraft("label");
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "configuration_unavailable" });
    ctl.saveDraft("label");
    await flush();
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1);
    gateway = false;
    ctl.reload();
    await flush();
    expect(ctl.snapshot().blocked).toBeNull();
    expect(ctl.snapshot().fields.label).toEqual({ draft: "Mine" });
    ctl.saveDraft("label");
    await flush();
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2);
  });

  it("a 200 with an unreadable body is unconfirmed: no second PATCH until Reload", async () => {
    let malformed = true;
    const calls: ConfigurationRequest[] = [];
    const source = httpConfigurationSource({
      endpoint: "/api/v1/example/settings",
      transport: async (_endpoint, request) => {
        calls.push(request);
        if (request.method === "GET") return { status: 200, body: example };
        return { status: 200, body: malformed ? null : commit(example, JSON.parse(request.body!)) };
      },
    });
    const ctl = new ConfigurationController(source);
    ctl.start();
    await flush();
    ctl.setDraft("label", "Mine");
    ctl.saveDraft("label");
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
    ctl.saveDraft("label");
    await flush();
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1);
    malformed = false;
    ctl.reload();
    await flush();
    expect(ctl.snapshot().blocked).toBeNull();
    ctl.saveDraft("label");
    await flush();
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2);
  });

  it("a transport that throws passes its exception through", async () => {
    const source = httpConfigurationSource({ endpoint: "/x", transport: async () => { throw new TypeError("Failed to fetch"); } });
    await expect(source.read()).rejects.toBeInstanceOf(TypeError);
  });

  it("a form over it treats an answer for another resource as unknown, not as that resource's document", async () => {
    const { source } = http((r) => ({ status: 200, body: r.method === "GET" ? example : { ...example, id: "other" } }));
    const ctl = new ConfigurationController(source);
    ctl.start();
    await flush();
    ctl.choose("layout", "dense");
    await flush();
    expect(ctl.snapshot().blocked).toEqual({ code: "unconfirmed" });
    expect(ctl.snapshot().doc?.id).toBe("example-preferences");
  });
});
