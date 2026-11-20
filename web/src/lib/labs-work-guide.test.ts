import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";
import { MANAGER_HEADER } from "./manager-choice";
import { parseDocument } from "./configuration/contract";
import { workGuideLabsSource } from "./labs-work-guide";

vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: vi.fn() }));
afterEach(() => vi.resetAllMocks());

it("binds the Labs read, save and reset to the manager whose form was opened", async () => {
  const manager = "example";
  const source = workGuideLabsSource(manager);
  const request = vi.mocked(api);
  request.mockResolvedValueOnce({ manager, work_guide: false });
  const { doc } = parseDocument(await source.read());
  expect(doc.values.work_guide).toBe(false);
  expect(request).toHaveBeenCalledWith("/assistant", expect.objectContaining({ headers: { [MANAGER_HEADER]: manager } }));
  request.mockResolvedValueOnce({ manager, work_guide: true });
  const { doc: saved } = parseDocument(await source.update({ ...doc, changes: { work_guide: true } }));
  expect(saved.values.work_guide).toBe(true);
  expect(saved.notices?.work_guide?.kind).toBe("info");
  expect(request).toHaveBeenLastCalledWith("/assistant", expect.objectContaining({
    headers: { [MANAGER_HEADER]: manager }, json: { work_guide: true, expected_work_guide: false },
  }));
  request.mockResolvedValueOnce({ manager, work_guide: false });
  expect(parseDocument(await source.update({ ...saved, reset: ["work_guide"] })).doc.values.work_guide).toBe(false);
  expect(request).toHaveBeenLastCalledWith("/assistant", expect.objectContaining({
    json: { work_guide: false, expected_work_guide: true },
  }));
});

it("leaves older managers read-only and reports a mismatched save as unconfirmed", async () => {
  const source = workGuideLabsSource("example");
  vi.mocked(api).mockResolvedValueOnce({ manager: "example" });
  const {doc} = parseDocument(await source.read());
  expect(doc.groups[0].fields[0].read_only).toBe(true);
  vi.mocked(api).mockResolvedValueOnce({ manager: "other", work_guide: true });
  await expect(source.update({ schema_version: 1, id: doc.id, revision: "false", changes: { work_guide: true } }))
    .rejects.toMatchObject({ code: "configuration_unavailable" });
});
