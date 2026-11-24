// The Health home (#13273) keeps /health/new as an alias and past chats at /health/<id>.
import { describe, expect, it } from "vitest";
import { registerModule } from "../../core/module-host";
import { health } from "./module";
import { healthRoute } from "./Health";
import { href, parse } from "../../lib/router";

registerModule(health);

describe("entering Health", () => {
  it("preserves the home alias and conversation addresses", () => {
    expect(parse("/health/new")).toEqual(healthRoute({ fresh: true }));
    expect(href(healthRoute({ fresh: true }))).toBe("/health/new");
    expect(parse("/health")).toEqual(healthRoute());
    expect(href(healthRoute())).toBe("/health");
    expect(parse("/health/h1")).toEqual(healthRoute({ sid: "h1" }));
  });
});
