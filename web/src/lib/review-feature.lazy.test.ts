/// <reference types="node" />
// The chat (ChatView, TurnChanges: first-screen files) asks whether the hub has git/review before it offers Review.
// It needs only the feature's name: importing it from session-review.ts put that module (review loading, comment
// drafts, hunk rows) on every first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import * as review from "./session-review";
import { REVIEW_FEATURE } from "./types";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const importsReview = /^import\s+(?!type\b)[^;]*from\s+["'][./]*lib\/session-review["']/m;

describe("the git/review feature's name", () => {
  it("keeps session-review.ts out of the chat's runtime imports", () => {
    expect(read("../views/chat/ChatView.tsx")).not.toMatch(importsReview);
    expect(read("../views/chat/TurnChanges.tsx")).not.toMatch(importsReview);
  });

  it("is the name session-review.ts gives the review pages", () => {
    expect(REVIEW_FEATURE).toBe("git_review");
    expect(review.REVIEW_FEATURE).toBe(REVIEW_FEATURE);
  });
});
