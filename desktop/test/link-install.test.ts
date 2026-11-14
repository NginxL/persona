import assert from "node:assert/strict";
import test from "node:test";
import { personaLinkExecutable } from "../src/link.js";

test("the connector is a fixed separate installation, not a renderer-chosen executable", () => {
  assert.equal(personaLinkExecutable("/home/example"), "/home/example/.persona/bin/persona-link");
});
