import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ComposerFile } from "../../lib/composer-files";
import { FileTiles } from "./FileTiles";

// A file's ✕ on a phone (#11442): its tap area is the ✕'s column of the tile (touch-hit-corner reaching 8 px left and
// 24 px down), not a square centred on it that took the end of a failed file's retry. The tile still clips it: reaching
// out past its side would lengthen the row's sideways scroll. Measured in a browser:
// scripts/test/web_mobile_input_e2e.py (tile_hits).
const file = (state: ComposerFile["state"]): ComposerFile => ({ id: state, name: `${state}.txt`, size: 12, sent: 0, state });

describe("FileTiles", () => {
  it("gives a file's ✕ its column for a tap area", () => {
    const html = renderToStaticMarkup(<FileTiles draftKey="k" files={[file("ready"), file("failed")]} />);
    const removes = [...html.matchAll(/<button[^>]*data-remove[^>]*class="([^"]*)"/g)].map((m) => m[1].split(" "));
    expect(removes).toHaveLength(2);
    for (const c of removes) {
      expect(c).toEqual(expect.arrayContaining(["touch-hit-corner", "[--hit-in-x:8px]", "[--hit-in-y:24px]"]));
      expect(c).not.toContain("touch-hit");
    }
    // the tiles keep clipping it
    expect(html.match(/class="group\/att relative [^"]*overflow-hidden/g)).toHaveLength(2);
  });
});
