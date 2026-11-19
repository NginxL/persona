// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { Item } from "../../lib/types";
import { publishTranscript, unpublishTranscript, useSharedTranscript } from "./transcript-share";
import type { Transcript } from "./useTranscript";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
let shown: Transcript | null;

function Probe() {
  shown = useSharedTranscript("m", "s");
  return null;
}

const page = (seq: number): Transcript => ({
  items: [{ id: `i${seq}#0`, seq, role: "user", kind: "text", text: `page ${seq}` } as Item],
  loading: false,
  firstRead: true,
  error: "",
  hasMore: false,
  loadingEarlier: false,
  loadEarlier: async () => undefined,
  expandFold: async () => true,
  streaming: "",
  notLive: false,
  reload: () => {},
});

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Probe />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

it("follows the owner's current history and only lets that owner unpublish it", () => {
  const owner = {};
  const other = {};
  act(() => publishTranscript(owner, "m", "s", page(4)));
  expect(shown?.items[0].text).toBe("page 4");

  act(() => publishTranscript(owner, "m", "s", page(1)));
  expect(shown?.items[0].text).toBe("page 1");
  act(() => unpublishTranscript(other, "m", "s"));
  expect(shown?.items[0].text).toBe("page 1");

  act(() => unpublishTranscript(owner, "m", "s"));
  expect(shown).toBeNull();
});
