// Terminal escape stripping for text written by a shell, a CLI or an agent: launcher errors, notebook output. Colour and
// cursor codes are CSI, titles, hyperlinks and shell integration are OSC, DCS / SOS / PM / APC carry other payloads, and
// the rest are two-byte ESC sequences. The C1 forms (U+0080–U+009F) exist too. Everything but LF and TAB goes, and CR or
// CRLF becomes LF. Pure.

// OSC: ends at BEL, ST (ESC \ or U+009C) or, unterminated, the end of the line or text.
const OSC = /\u001b\][^\n]*?(?:\u0007|\u001b\\|\u009c|(?=\n)|$)/g;
const C1_OSC = /\u009d[^\n]*?(?:\u0007|\u009c|(?=\n)|$)/g;

// DCS, SOS, PM and APC: the same terminators.
const STRING = /\u001b[PX^_][^\n]*?(?:\u0007|\u001b\\|\u009c|(?=\n)|$)/g;
const C1_STRING = /[\u0090\u0098\u009e\u009f][^\n]*?(?:\u0007|\u009c|(?=\n)|$)/g;

// CSI: parameters, intermediates, final byte.
const CSI = /\u001b\[[0-9:;<=>?]*[ -/]*[@-~]/g;
const C1_CSI = /\u009b[0-9:;<=>?]*[ -/]*[@-~]/g;

// A CSI cut off before its final byte: its parameters go too, or they would read as text.
const CSI_TAIL = /\u001b\[[0-9:;<=>?]*[ -/]*/g;
const C1_CSI_TAIL = /\u009b[0-9:;<=>?]*[ -/]*/g;

// Everything else ESC introduces: intermediates and one final byte (`\x1b=`, `\x1b(B`, `\x1b7`…). A lone ESC is left to
// the control pass below.
const ESC = /\u001b[ -/]*[0-~]/g;

// C0 (but LF and TAB), DEL and the C1 controls.
// eslint-disable-next-line no-control-regex
const CONTROLS = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g;

/** `s` without terminal escapes and control characters: CR and CRLF become LF, LF and TAB stay, invalid-looking bytes are
 *  untouched. A string with nothing to strip comes back unchanged. */
export function stripAnsi(s: string): string {
  return s
    .replace(/\r\n/g, "\n")
    .replace(/\r/g, "\n")
    .replace(OSC, "")
    .replace(C1_OSC, "")
    .replace(STRING, "")
    .replace(C1_STRING, "")
    .replace(CSI, "")
    .replace(C1_CSI, "")
    .replace(CSI_TAIL, "")
    .replace(C1_CSI_TAIL, "")
    .replace(ESC, "")
    .replace(CONTROLS, "");
}
