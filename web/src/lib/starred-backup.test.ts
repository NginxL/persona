import { describe, expect, it } from "vitest";
import { decodeStarredBackup, parseStarredBackup, restoredStarKey, starredBackup, starredMarkdown } from "./starred-backup";
import type { Star } from "./starred-rows";

const key = "sm" + "a".repeat(32);
const star: Star = {
  id: key, machine_id: "private-device", session_id: "private-session", item_id: "line42#0",
  conversation: "assistant", role: "assistant", title: "Release notes", speaker: "Persona", source_manager: "private-manager",
  source_fingerprint: "e".repeat(32), text: "Save this. 🐈", starred_at: "2026-11-21T03:04:00Z",
  written_at: "2026-11-20T03:04:00Z", omitted_chars: 7,
};

describe("Starred backup files", () => {
  it("round-trips the portable fields without transcript or manager identifiers", () => {
    const backup = starredBackup([star]);
    const raw = JSON.stringify(backup);
    expect(parseStarredBackup(raw)).toEqual(backup);
    expect(backup).toEqual({ format: "persona.starred", version: 1, messages: [{
      key, conversation: "assistant", role: "assistant", title: "Release notes", speaker: "Persona", text: "Save this. 🐈",
      starred_at: star.starred_at, written_at: star.written_at, omitted_chars: 7,
    }] });
    expect(raw).not.toMatch(/machine_id|session_id|item_id|source_manager|source_fingerprint|private-/);
  });

  it("recognizes a restored snapshot only when all parts of the archive marker match", () => {
    const imported = {
      ...star, id: "sm" + "b".repeat(32), machine_id: "persona-starred-backup", session_id: "backup." + key,
      item_id: "line0#0", source_fingerprint: undefined, source_manager: undefined,
    };
    expect(restoredStarKey(star)).toBeUndefined();
    expect(restoredStarKey(imported)).toBe(key);
    expect(starredBackup([imported]).messages[0].key).toBe(key);
    expect(restoredStarKey({ ...imported, session_id: "backup.not-a-key" })).toBeUndefined();
    expect(restoredStarKey({ ...imported, source_fingerprint: star.source_fingerprint })).toBeUndefined();
    expect(restoredStarKey({ ...imported, source_manager: star.source_manager })).toBeUndefined();
  });

  it("rejects unrelated JSON, unsupported versions, and extra source data before any upload", () => {
    const backup = starredBackup([star]);
    const encode = (value: unknown) => JSON.stringify(value);
    expect(parseStarredBackup("not JSON")).toBeUndefined();
    expect(parseStarredBackup(encode({ ...backup, version: 2 }))).toBeUndefined();
    expect(parseStarredBackup(encode({ ...backup, metadata: "secret" }))).toBeUndefined();
    expect(parseStarredBackup(encode({ ...backup, messages: [{ ...backup.messages[0], machine_id: star.machine_id }] }))).toBeUndefined();
    expect(parseStarredBackup(encode({ ...backup, messages: [{ ...backup.messages[0], key: "invalid" }] }))).toBeUndefined();
  });

  it("validates UTF-8 strictly and retains the original JSON for preview", () => {
    expect(decodeStarredBackup(new Uint8Array([0x7b, 0xc3, 0x28]).buffer)).toBeUndefined();
    const backup = starredBackup([star]);
    const raw = JSON.stringify(backup, null, 2) + "\n";
    expect(decodeStarredBackup(new TextEncoder().encode(raw).buffer)).toEqual({ raw, bundle: backup });
  });

  it("exports readable Markdown while keeping literal message fences and author metadata", () => {
    const markdown = starredMarkdown([{ ...star, text: "Line one\n````\nLine three" }], {
      title: "Starred", conversation: "Conversation", speaker: (star) => star.speaker, omitted: (count) => `${count} more characters`,
    });
    expect(markdown).toBe("# Starred\n\n## Release notes\n\nPersona · 2026-11-20T03:04:00Z\n\n`````\nLine one\n````\nLine three\n`````\n\n7 more characters\n");
    expect(markdown).not.toMatch(/private-|sm[a-f0-9]{32}/);
  });
});
