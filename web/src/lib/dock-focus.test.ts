// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { COMPOSER_INPUT, focusTaken, lostWith } from "./dock-focus";

afterEach(() => {
  document.body.innerHTML = "";
});

describe("lostWith (#5154)", () => {
  it("is lost when the focused button left the page and the body has the focus", () => {
    const b = document.createElement("button");
    document.body.append(b);
    b.focus();
    b.remove();
    expect(lostWith(b, document.activeElement, document.body)).toBe(true);
  });

  it("is lost when the button was disabled while it sends", () => {
    const b = document.createElement("button");
    document.body.append(b);
    b.disabled = true;
    expect(lostWith(b, document.body, document.body)).toBe(true);
  });

  it("is lost when its fieldset was disabled", () => {
    const f = document.createElement("fieldset");
    const b = document.createElement("button");
    f.append(b);
    document.body.append(f);
    f.disabled = true;
    expect(lostWith(b, null, document.body)).toBe(true);
  });

  it("is not lost while something else has the focus", () => {
    const b = document.createElement("button");
    const other = document.createElement("textarea");
    document.body.append(other);
    expect(lostWith(b, other, document.body)).toBe(false);
  });

  it("is not lost when the button is still there and usable (a click on the page's text)", () => {
    const b = document.createElement("button");
    document.body.append(b);
    expect(lostWith(b, document.body, document.body)).toBe(false);
  });

  it("is not lost when nothing above the composer had the focus", () => {
    expect(lostWith(null, document.body, document.body)).toBe(false);
  });
});

describe("focusTaken (#11237)", () => {
  it("is taken by another field or a dialog, not by the body or the composer itself", () => {
    document.body.innerHTML = `<textarea id="own" data-composer-input></textarea><input id="other" /><div role="dialog"><button id="in-dialog">OK</button></div><button id="plain">Go</button>`;
    const own = document.getElementById("own");
    const at = (id: string) => document.getElementById(id);
    expect(focusTaken(document.body, own)).toBe(false);
    expect(focusTaken(own, own)).toBe(false);
    expect(focusTaken(at("other"), own)).toBe(true);
    expect(focusTaken(at("in-dialog"), own)).toBe(true);
    expect(focusTaken(at("plain"), own)).toBe(false);
    expect(document.querySelector(COMPOSER_INPUT)).toBe(own);
  });
});
