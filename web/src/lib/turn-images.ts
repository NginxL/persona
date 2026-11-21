// The pictures an agent looked at in one turn (#1540; iOS / Mac draw the same strip): images its tools brought back
// (a Read or Codex view_image of a picture, screenshots of the screen or a browser, an MCP tool's image) and pictures it named by reading
// a file whose image did not come back with it. The user's own pictures are in their bubble, not here. Each picture
// once (by path, else by item), in the order it first appeared. Pure; tested in turn-images.test.ts.
import { t } from "../i18n";
import type { Segment, Step } from "./turns";
import type { FoldImage, Item } from "./types";

export interface TurnImage {
  key: string;
  /** The image item the transcript carries (its bytes from items/{id}/image); missing: only a path is known. */
  item?: Item;
  /** On the machine, when it came from a file. */
  path?: string;
  /** What it is, for its label: the file's name, or "Screenshot" / "Web screenshot" / the MCP server. */
  label: string;
  /** What brought it: read | screenshot | browser | mcp | tool. */
  source: "read" | "screenshot" | "browser" | "mcp" | "tool";
  width?: number;
  height?: number;
}

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|bmp|heic|heif|avif|tiff?|svg)$/i;

function inputPath(use: Item): string | undefined {
  const i = use.tool_input ?? {};
  for (const k of ["file_path", "path", "Path", "image_path", "filepath"]) {
    const v = i[k];
    if (typeof v === "string" && v.trim()) return v.trim();
  }
  return undefined;
}

/** What kind of tool brought a picture, from its name (Read, mcp__cua__screenshot, browser_look, computer_state…). */
export function sourceOf(tool: string): TurnImage["source"] {
  const t = tool.toLowerCase();
  if (t === "read" || t.endsWith("__read") || t === "view" || t === "view_image" || t.endsWith("read_file")) return "read"; // view_image: Codex
  if (/browser|playwright|page_screenshot|look/.test(t)) return "browser";
  if (/screenshot|screen|computer|cua|capture|snap/.test(t)) return "screenshot";
  if (t.startsWith("mcp__")) return "mcp";
  return "tool";
}

function labelOf(source: TurnImage["source"], tool: string, path: string | undefined, machine: string | undefined): string {
  if (path) return path.split(/[\\/]/).pop() || path;
  const where = machine ? ` · ${machine}` : "";
  if (source === "screenshot") return t("lib-turn-images.screenshot") + where;
  if (source === "browser") return t("lib-turn-images.browser_screenshot") + where;
  if (source === "mcp") return t("lib-turn-images.image_from", { tool: tool.split("__")[1] ?? "MCP" });
  return t("lib-turn-images.image_from", { tool: tool || t("lib-turn-images.tool") });
}

/** A folded picture as the strip shows it (its bytes by item id, else its file). */
function fromFold(f: FoldImage, machine?: string): TurnImage {
  const where = f.machine || machine;
  const item: Item | undefined = f.item_id
    ? { id: f.item_id, seq: 0, role: "user", kind: "image", tool_use_id: f.tool_use_id, image: { mime: f.mime ?? "image/png", lazy: true, size: f.size, width: f.width, height: f.height } }
    : undefined;
  return { key: f.item_id ? `i:${f.item_id}` : `r:${f.tool_use_id}:${f.path ?? ""}`, item, path: f.path, source: f.source, label: labelOf(f.source, f.tool, f.path, where), width: f.width, height: f.height };
}

/**
 * The pictures of a turn (its work's steps, including runs folded away), first appearance first, each once: the same
 * path once, and without a path the same kind, size and dimensions once (the same screenshot read twice).
 */
export function turnImages(seg: Pick<Segment, "steps">, machine?: string): TurnImage[] {
  const out: TurnImage[] = [];
  const seen = new Set<string>();
  const add = (img: TurnImage) => {
    const im = img.item?.image;
    const id = img.path ? `p:${img.path}` : im?.size && img.width && img.height ? `s:${im.mime}:${im.size}:${img.width}x${img.height}` : img.key;
    if (seen.has(id)) return;
    seen.add(id);
    out.push(img);
  };
  const visit = (steps: Step[]) => {
    for (const st of steps) {
      if (st.kind === "tool") {
        const tool = st.use.tool_name ?? "";
        const source = sourceOf(tool);
        const path = source === "read" ? inputPath(st.use) : undefined;
        const filePath = path && IMAGE_EXT.test(path) ? path : undefined;
        if (st.images.length) {
          for (const im of st.images) {
            add({ key: `i:${im.id}`, item: im, path: st.images.length === 1 ? filePath : undefined, source, label: labelOf(source, tool, st.images.length === 1 ? filePath : undefined, machine), width: im.image?.width, height: im.image?.height });
          }
        } else if (filePath && st.result && !st.result.is_error) {
          // a picture read whose bytes the transcript does not carry: drawn from the file on the machine
          add({ key: `r:${st.use.id}`, path: filePath, source, label: labelOf(source, tool, filePath, machine) });
        }
      } else if (st.kind === "fold") {
        for (const f of st.item.fold?.images ?? []) add(fromFold(f, machine));
      } else if (st.kind === "image" && st.item.role !== "user") {
        add({ key: `i:${st.item.id}`, item: st.item, source: "tool", label: labelOf("tool", "", undefined, machine), width: st.item.image?.width, height: st.item.image?.height });
      }
    }
  };
  visit(seg.steps);
  return out;
}
