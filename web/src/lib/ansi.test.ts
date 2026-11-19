import { describe, expect, it } from "vitest";
import { stripAnsi } from "./ansi";

// Text a terminal wrote must read as plain text: no colour, no prompt records, no control bytes; line structure kept.
describe("stripAnsi", () => {
  it("leaves plain text alone", () => {
    expect(stripAnsi("")).toBe("");
    expect(stripAnsi("API Error: 401 invalid x-api-key")).toBe("API Error: 401 invalid x-api-key");
    expect(stripAnsi("进度 ok 😀\nsecond\tline")).toBe("进度 ok 😀\nsecond\tline");
  });

  it("drops colour and cursor CSI sequences, private and intermediate modes too", () => {
    expect(stripAnsi("\u001b[1;32mok\u001b[0m")).toBe("ok");
    expect(stripAnsi("\u001b[?25lhidden\u001b[?25h")).toBe("hidden");
    expect(stripAnsi("\u001b[38:2:255:0:0mred\u001b[m")).toBe("red");
    expect(stripAnsi("\u001b[1 qcursor")).toBe("cursor");
    expect(stripAnsi("a\u001b[1;3")).toBe("a"); // unterminated
  });

  it("drops OSC records: title, hyperlink, shell integration, clipboard", () => {
    expect(stripAnsi("a\u001b]0;title\u0007b")).toBe("ab");
    expect(stripAnsi("a\u001b]8;;https://example.test\u001b\\link\u001b]8;;\u001b\\b")).toBe("alinkb");
    expect(stripAnsi("\u001b]1337;RemoteHost=dev@workstation\u0007\u001b]1337;CurrentDir=/Users/dev\u0007done")).toBe("done");
    expect(stripAnsi("\u001b]52;c;aGVsbG8=\u0007x")).toBe("x");
    expect(stripAnsi("a\u001b]0;title\u009cb")).toBe("ab");
  });

  it("drops DCS, SOS, PM and APC payloads", () => {
    expect(stripAnsi("a\u001bPq#0;2;0;0;0\u001b\\b")).toBe("ab");
    expect(stripAnsi("a\u001bXsos\u001b\\b")).toBe("ab");
    expect(stripAnsi("a\u001b^pm\u001b\\b")).toBe("ab");
    expect(stripAnsi("a\u001b_apc\u001b\\b")).toBe("ab");
  });

  it("drops the C1 forms", () => {
    expect(stripAnsi("a\u009b1;32mok\u009b0mb")).toBe("aokb");
    expect(stripAnsi("a\u009d0;title\u0007b")).toBe("ab");
    expect(stripAnsi("a\u0090data\u009cb")).toBe("ab");
    expect(stripAnsi("a\u009fapc\u009cb")).toBe("ab");
  });

  it("drops two-byte ESC sequences and a lone ESC", () => {
    expect(stripAnsi("a\u001b=b")).toBe("ab");
    expect(stripAnsi("a\u001b(Bb")).toBe("ab");
    expect(stripAnsi("a\u001b7b\u001b8c")).toBe("abc");
    expect(stripAnsi("a\u001bMb")).toBe("ab");
    expect(stripAnsi("a\u001b")).toBe("a");
  });

  it("keeps line structure and drops other controls", () => {
    expect(stripAnsi("a\r\nb")).toBe("a\nb");
    expect(stripAnsi("a\rb")).toBe("a\nb");
    expect(stripAnsi("a\u0000\u0007\u000b\u000c\u001f\u007f\u0085b")).toBe("ab");
    expect(stripAnsi("a\tb\nc")).toBe("a\tb\nc");
    // U+00A0 is not a C1 control.
    expect(stripAnsi("a b")).toBe("a b");
  });

  it("handles an unterminated OSC at the end of the line or text without swallowing later lines", () => {
    expect(stripAnsi("before\u001b]1337;CurrentDir=/Users/dev")).toBe("before");
    expect(stripAnsi("\u001b]1337;CurrentDir=/Users/dev\nError: no such file")).toBe("\nError: no such file");
    expect(stripAnsi("\u009d1337;CurrentDir=/Users/dev\nError: no such file")).toBe("\nError: no such file");
  });
});
