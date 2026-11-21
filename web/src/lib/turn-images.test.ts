import { describe, expect, it } from "vitest";
import { sourceOf, turnImages } from "./turn-images";
import type { Step } from "./turns";
import type { Item } from "./types";

const item = (o: Partial<Item>): Item => ({ id: o.id ?? "x", seq: 1, role: "assistant", kind: "text", ...o }) as Item;
const tool = (id: string, name: string, input: Record<string, unknown>, images: Item[], result = true): Step => ({
  kind: "tool",
  id,
  use: item({ id, kind: "tool_use", tool_name: name, tool_input: input }),
  result: result ? item({ id: `${id}r`, kind: "tool_result" }) : undefined,
  images,
});
const img = (id: string, w = 800, h = 600) => item({ id, kind: "image", role: "user", tool_use_id: "t", image: { mime: "image/png", lazy: true, width: w, height: h } });

describe("turnImages", () => {
  it("gathers what the tools brought back, labeled, each once, in order", () => {
    const steps: Step[] = [
      tool("a", "Read", { file_path: "/Users/me/code/weather/docs/icon.png" }, [img("i1", 512, 512)]),
      tool("b", "mcp__cua__screenshot", {}, [img("i2", 2880, 1800)]),
      tool("c", "Read", { file_path: "/Users/me/code/weather/docs/icon.png" }, [img("i3", 512, 512)]), // the same file again
      tool("d", "mcp__playwright__browser_take_screenshot", {}, [img("i4")]),
      tool("e", "Read", { file_path: "/Users/me/code/weather/README.md" }, []), // not a picture
      tool("f", "Read", { file_path: "/tmp/plot.png" }, []), // a picture whose bytes are not in the transcript
      tool("g", "Read", { file_path: "/tmp/missing.png" }, [], false), // still running / failed: nothing yet
      tool("h", "mcp__figma__get_image", {}, [img("i5")]),
      tool("k", "view_image", { path: "/Users/me/shots/codex.png" }, [img("i6", 1200, 800)]), // Codex
      tool("l", "read_file", { Path: "C:\\Users\\me\\charts\\aa.png" }, []), // Antigravity read_file
    ];
    const got = turnImages({ steps }, "MacBook");
    expect(got.map((g) => [g.key, g.label, g.source])).toEqual([
      ["i:i1", "icon.png", "read"],
      ["i:i2", "截图 · MacBook", "screenshot"],
      ["i:i4", "网页截图 · MacBook", "browser"],
      ["r:f", "plot.png", "read"],
      ["i:i5", "figma 返回的图片", "mcp"],
      ["i:i6", "codex.png", "read"],
      ["r:l", "aa.png", "read"],
    ]);
    expect(got[0]).toMatchObject({ path: "/Users/me/code/weather/docs/icon.png", width: 512, height: 512 });
    expect(got[3].item).toBeUndefined();
  });

  it("leaves the user's own pictures to their bubble", () => {
    const own = item({ id: "u", kind: "image", role: "user", image: { mime: "image/png" } });
    const agent = item({ id: "s", kind: "image", role: "assistant", image: { mime: "image/png" } });
    expect(turnImages({ steps: [{ kind: "image", id: "u", item: own }, { kind: "image", id: "s", item: agent }] }).map((g) => g.key)).toEqual(["i:s"]);
  });

  it("names where a picture came from by its tool", () => {
    expect(["Read", "view_image", "mcp__cua__computer_state", "browser_look", "mcp__x__thing", "Bash"].map(sourceOf)).toEqual(["read", "read", "screenshot", "browser", "mcp", "tool"]);
  });
});

describe("folded runs", () => {
  it("reads proto.Fold.images: item ids, paths, sources and machines, deduped", () => {
    const fold = item({
      id: "fold:x",
      kind: "fold",
      fold: {
        items: 9,
        tools: 4,
        thoughts: 0,
        through_seq: 9,
        images: [
          { item_id: "im1", tool_use_id: "t1", tool: "mcp__cua__screenshot", source: "screenshot", machine: "Mac mini", mime: "image/png", size: 4000, width: 1440, height: 900 },
          { tool_use_id: "t2", tool: "Read", source: "read", path: "/tmp/plot.png" },
          { item_id: "im3", tool_use_id: "t3", tool: "mcp__cua__screenshot", source: "screenshot", mime: "image/png", size: 4000, width: 1440, height: 900 }, // the same screenshot again
          { item_id: "im4", tool_use_id: "t4", tool: "Read", source: "read", path: "/tmp/plot.png", mime: "image/png" }, // the same file again
        ],
      },
    });
    const got = turnImages({ steps: [{ kind: "fold", id: "fold:x", item: fold }] }, "MacBook");
    expect(got.map((g) => [g.key, g.label, g.source, g.item?.id])).toEqual([
      ["i:im1", "截图 · Mac mini", "screenshot", "im1"],
      ["r:t2:/tmp/plot.png", "plot.png", "read", undefined],
    ]);
    expect(got[0].item?.image).toMatchObject({ lazy: true, width: 1440, height: 900 });
  });
});
