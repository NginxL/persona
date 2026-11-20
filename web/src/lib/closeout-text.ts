// The Markdown heading, three required facts and final rule are the versioned terminal report.
const REPORT = /(?:^|\n)(### Persona (?:work report|工作报告|作業報告)\r?\n(?:\r?\n)?- Status: (?:done|not_done|blocked)\r?\n- Merged: (?:yes|no|n\/a)\r?\n- Deployed: (?:yes|no|n\/a)\r?\n(?:- (?:Did|Artifact|Delivery|Unblock|Verified|Missing|Waiting|Next): [^\r\n]+\r?\n)*(?:\r?\n)?---)(?=[\t \r\n]*(?:\n<oai-mem-citation>[\s\S]*?<\/oai-mem-citation>[\t \r\n]*)?$)/;
const REPORT_FENCE = /^[\t ]*(`{3,}|~{3,})([^\r\n]*)/gm;

function insideReportFence(before: string): boolean {
  let open = "";
  for (const [, fence, rest] of before.matchAll(REPORT_FENCE)) {
    if (!open) open = fence;
    else if (fence.startsWith(open) && !rest.trim()) open = "";
  }
  return Boolean(open);
}

const BLOCK = /<closeout\b[^>]*>[\s\S]*?<\/closeout>/g;

/**
 * `text` without its last closeout block, the one the hub read (server/internal/closeout takes the last): the card
 * above the transcript says it, so the reply does not say it again as raw text. Only the block goes, with the blank
 * lines it leaves and a code fence that held nothing else; text without a block comes back as it is.
 */
export function stripCloseout(text: string, complete = true): string {
  const report = complete && text.includes("### Persona ") && REPORT.exec(text);
  if (report && !insideReportFence(text.slice(0, report.index))) {
    return [text.slice(0, report.index).trimEnd(), text.slice(report.index + report[0].length).trimStart()].filter(Boolean).join("\n\n");
  }
  let last: RegExpExecArray | undefined;
  BLOCK.lastIndex = 0;
  for (let m = BLOCK.exec(text); m; m = BLOCK.exec(text)) last = m;
  if (!last) return text;
  let before = text.slice(0, last.index);
  let after = text.slice(last.index + last[0].length);
  // a fence around the block alone goes with it: an empty code box says nothing
  const open = /(^|\n)[ \t]*(`{3,}|~{3,})[^\n`]*\n[ \t]*$/.exec(before);
  const close = /^[ \t]*\n[ \t]*(`{3,}|~{3,})[ \t]*(?=\n|$)/.exec(after);
  if (open && close && open[2] === close[1]) {
    before = before.slice(0, open.index);
    after = after.slice(close[0].length);
  }
  before = before.replace(/\s+$/, "");
  after = after.replace(/^\s+/, "");
  return before && after ? `${before}\n\n${after}` : before || after;
}
