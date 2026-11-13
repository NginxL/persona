/** A small, dependency-free Markdown tree for chat and resource text. */
export type MarkdownInlineKind = 'text' | 'bold' | 'italic' | 'code' | 'link';
export type MarkdownBlockKind = 'paragraph' | 'heading' | 'code' | 'rule' | 'quote' | 'list' | 'list-item' | 'table' | 'raw';

export class MarkdownInline {
  kind: MarkdownInlineKind = 'text';
  text: string = '';
  url: string = '';
  source: string = '';
  children: MarkdownInline[] = [];

  constructor(kind: MarkdownInlineKind = 'text', text: string = '', url: string = '', children: MarkdownInline[] = [], source: string = '') {
    this.kind = kind;
    this.text = text;
    this.url = url;
    this.children = children;
    this.source = source;
  }
}

export class MarkdownTableRow {
  cells: MarkdownInline[][] = [];
  header: boolean = false;
}

export class MarkdownBlock {
  kind: MarkdownBlockKind = 'paragraph';
  source: string = '';
  text: string = '';
  language: string = '';
  marker: string = '';
  ordered: boolean = false;
  level: number = 0;
  children: MarkdownBlock[] = [];
  items: MarkdownBlock[] = [];
  rows: MarkdownTableRow[] = [];
  alignments: string[] = [];
  inlines: MarkdownInline[] = [];

  constructor(kind: MarkdownBlockKind = 'paragraph', source: string = '') {
    this.kind = kind;
    this.source = source;
  }
}

class ListMarker {
  indent: number = 0;
  marker: string = '';
  content: string = '';
  contentIndent: number = 0;
  ordered: boolean = false;
}

class FenceMarker {
  character: string = '`';
  length: number = 3;
  language: string = '';
}

class LinkCandidate {
  end: number = 0;
  source: string = '';
  label: string = '';
  url: string = '';
}

function listMarker(line: string): ListMarker | undefined {
  const match = /^( *)([-+*]|[0-9]{1,9}[.)])(?:([ \t]+)(.*)|([ \t]*))$/.exec(line);
  if (!match || match[1].length > 3) return undefined;
  const marker = new ListMarker();
  marker.indent = match[1].length;
  marker.marker = match[2];
  marker.content = match[4] || '';
  const spacing = match[3] || ' ';
  marker.contentIndent = marker.indent + marker.marker.length + spacing.length;
  marker.ordered = /^[0-9]/.test(marker.marker);
  return marker;
}

function fenceMarker(line: string): FenceMarker | undefined {
  const match = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
  if (!match) return undefined;
  const character = match[1][0];
  const info = match[2].trim();
  if (character === '`' && info.includes('`')) return undefined;
  const marker = new FenceMarker();
  marker.character = character;
  marker.length = match[1].length;
  marker.language = info.split(/[ \t]+/)[0] || '';
  return marker;
}

function isFenceClose(line: string, fence: FenceMarker): boolean {
  const body = line.replace(/^ {0,3}/, '');
  let count = 0;
  while (count < body.length && body[count] === fence.character) count++;
  return count >= fence.length && body.slice(count).trim().length === 0;
}

function headingMatch(line: string): RegExpExecArray | null {
  return /^( {0,3})(#{1,6})(?:[ \t]+|$)(.*)$/.exec(line);
}

function headingBody(value: string): string {
  return value.replace(/[ \t]+#+[ \t]*$/, '').trim();
}

function isRule(line: string): boolean {
  return /^( {0,3})(?:\*[ \t]*){3,}$/.test(line) || /^( {0,3})(?:-[ \t]*){3,}$/.test(line) || /^( {0,3})(?:_[ \t]*){3,}$/.test(line);
}

function quotePrefix(line: string): RegExpExecArray | null {
  return /^ {0,3}>[ \t]?/.exec(line);
}

function splitTableCells(line: string): string[] {
  let value = line.trim();
  if (value.startsWith('|')) value = value.slice(1);
  if (value.endsWith('|') && !endsWithEscapedPipe(value)) value = value.slice(0, -1);
  const cells: string[] = [];
  let cell = '';
  let codeTicks = 0;
  for (let index = 0; index < value.length;) {
    const character = value[index];
    if (character === '\\' && index + 1 < value.length) {
      cell += character + value[index + 1];
      index += 2;
      continue;
    }
    if (character === '`') {
      let end = index + 1;
      while (end < value.length && value[end] === '`') end++;
      const count = end - index;
      if (codeTicks === 0) codeTicks = count;
      else if (codeTicks === count) codeTicks = 0;
      cell += value.slice(index, end);
      index = end;
      continue;
    }
    if (character === '|' && codeTicks === 0) {
      cells.push(cell.trim());
      cell = '';
    } else cell += character;
    index++;
  }
  cells.push(cell.trim());
  return cells;
}

function endsWithEscapedPipe(value: string): boolean {
  if (!value.endsWith('|')) return false;
  let slashes = 0;
  for (let index = value.length - 2; index >= 0 && value[index] === '\\'; index--) slashes++;
  return slashes % 2 === 1;
}

function hasTableSeparator(line: string): boolean {
  const trimmed = line.trim();
  return !/^ {4}/.test(line) && (splitTableCells(line).length > 1 || trimmed.startsWith('|') ||
    (trimmed.endsWith('|') && !endsWithEscapedPipe(trimmed)));
}

function tableAlignments(line: string, columns: number): string[] | undefined {
  if (/^ {4}/.test(line)) return undefined;
  const cells = splitTableCells(line);
  if (cells.length !== columns || columns < 1) return undefined;
  const result: string[] = [];
  for (const cell of cells) {
    if (!/^:?-{1,}:?$/.test(cell)) return undefined;
    const left = cell.startsWith(':');
    const right = cell.endsWith(':');
    result.push(left && right ? 'center' : right ? 'end' : 'start');
  }
  return result;
}

function safeHttpUrl(value: string): boolean {
  if (!/^https?:\/\//i.test(value)) return false;
  if (/[\u0000-\u0020\u007f<>"'\\]/.test(value)) return false;
  const authority = value.replace(/^https?:\/\//i, '').split(/[/?#]/, 1)[0];
  const host = authority.slice(authority.lastIndexOf('@') + 1);
  return host.length > 0 && !host.startsWith(':') && !host.startsWith('.') && !host.endsWith('.');
}

function findBracketEnd(value: string, start: number): number {
  let depth = 0;
  for (let index = start + 1; index < value.length; index++) {
    if (value[index] === '\\') { index++; continue; }
    if (value[index] === '`') {
      const run = delimiterRun(value, index, '`');
      const close = findCodeEnd(value, index, run);
      if (close >= 0) { index = close + run - 1; continue; }
    }
    if (value[index] === '[') depth++;
    else if (value[index] === ']') {
      if (depth === 0) return index;
      depth--;
    }
  }
  return -1;
}

function linkCandidate(value: string, start: number): LinkCandidate | undefined {
  const closeLabel = findBracketEnd(value, start);
  if (closeLabel < 0 || value[closeLabel + 1] !== '(') return undefined;
  const urlStart = closeLabel + 2;
  let close = -1;
  if (value[urlStart] === '<') {
    const angleEnd = value.indexOf('>', urlStart + 1);
    if (angleEnd < 0 || value[angleEnd + 1] !== ')') return undefined;
    close = angleEnd + 1;
  } else {
    let depth = 0;
    for (let index = urlStart; index < value.length; index++) {
      if (value[index] === '\\') { index++; continue; }
      if (value[index] === '(') depth++;
      else if (value[index] === ')') {
        if (depth === 0) { close = index; break; }
        depth--;
      }
    }
    if (close < 0) return undefined;
  }
  const source = value.slice(start, close + 1);
  let url = value.slice(urlStart, close);
  if (url.startsWith('<') && url.endsWith('>')) url = url.slice(1, -1);
  const candidate = new LinkCandidate();
  candidate.end = close + 1;
  candidate.source = source;
  candidate.label = value.slice(start + 1, closeLabel);
  candidate.url = url;
  return candidate;
}

function delimiterRun(value: string, start: number, delimiter: string): number {
  let end = start;
  while (end < value.length && value[end] === delimiter) end++;
  return end - start;
}

function findCodeEnd(value: string, start: number, run: number): number {
  let index = start + run;
  while (index < value.length) {
    const next = value.indexOf('`', index);
    if (next < 0) return -1;
    const count = delimiterRun(value, next, '`');
    if (count === run) return next;
    index = next + count;
  }
  return -1;
}

function normalizeInlineCode(value: string): string {
  let result = value.replace(/\n/g, ' ');
  if (result.length > 2 && result.startsWith(' ') && result.endsWith(' ') && result.trim().length > 0)
    result = result.slice(1, -1);
  return result;
}

function escapedAt(value: string, index: number): boolean {
  let slashes = 0;
  for (let at = index - 1; at >= 0 && value[at] === '\\'; at--) slashes++;
  return slashes % 2 === 1;
}

function emphasisClose(value: string, start: number, delimiter: string): number {
  let index = start + delimiter.length;
  while (index < value.length) {
    if (value[index] === '\\') { index += 2; continue; }
    if (value[index] === '`') {
      const run = delimiterRun(value, index, '`');
      const close = findCodeEnd(value, index, run);
      if (close >= 0) { index = close + run; continue; }
    }
    const delimiterLength = delimiterRun(value, index, delimiter[0]);
    if (delimiterLength > 0) {
      if (escapedAt(value, index)) { index += delimiterLength; continue; }
      if (delimiterLength !== delimiter.length) { index += delimiterLength; continue; }
      const before = value[index - 1] || '';
      const after = value[index + delimiter.length] || '';
      if (before.trim().length > 0 && (delimiter.length > 1 || after !== delimiter[0])) return index;
      index += delimiterLength;
      continue;
    }
    index++;
  }
  return -1;
}

function isWordCharacter(value: string): boolean {
  return /^[A-Za-z0-9]$/.test(value);
}

function trimBareUrl(value: string): string {
  let result = value.replace(/[.,;:!?，。！？；：、"']+$/, '');
  let changed = true;
  while (changed) {
    changed = false;
    if (result.endsWith(')') && (result.match(/\)/g) || []).length > (result.match(/\(/g) || []).length) {
      result = result.slice(0, -1); changed = true;
    }
    if (result.endsWith(']') && (result.match(/\]/g) || []).length > (result.match(/\[/g) || []).length) {
      result = result.slice(0, -1); changed = true;
    }
    if (result.endsWith('}') && (result.match(/\}/g) || []).length > (result.match(/\{/g) || []).length) {
      result = result.slice(0, -1); changed = true;
    }
  }
  return result;
}

function bareUrlAt(value: string, start: number): string {
  if (start > 0 && isWordCharacter(value[start - 1])) return '';
  if (!value.startsWith('http://', start) && !value.startsWith('https://', start)) return '';
  let end = start;
  while (end < value.length && value.charCodeAt(end) <= 0x7e && !/[\s<>"'`]/.test(value[end])) end++;
  const url = trimBareUrl(value.slice(start, end));
  return safeHttpUrl(url) ? url : '';
}

function appendPlain(nodes: MarkdownInline[], text: string): void {
  if (!text) return;
  const last = nodes[nodes.length - 1];
  if (last && last.kind === 'text') {
    last.text += text;
    last.source += text;
  } else nodes.push(new MarkdownInline('text', text, '', [], text));
}

export function parseInlineMarkdown(value: string): MarkdownInline[] {
  const nodes: MarkdownInline[] = [];
  let plain = '';
  const flush = (): void => { appendPlain(nodes, plain); plain = ''; };
  for (let index = 0; index < value.length;) {
    if (value[index] === '\\' && index + 1 < value.length && /[\\`*_[\]{}()#+.!<>|~-]/.test(value[index + 1])) {
      plain += value[index + 1];
      index += 2;
      continue;
    }
    if (value[index] === '`') {
      const run = delimiterRun(value, index, '`');
      const close = findCodeEnd(value, index, run);
      if (close >= 0) {
        flush();
        const end = close + run;
        const source = value.slice(index, end);
        nodes.push(new MarkdownInline('code', normalizeInlineCode(value.slice(index + run, close)), '', [], source));
        index = end;
        continue;
      }
    }
    const imagePrefix = value[index] === '!' && value[index + 1] === '[';
    const linkStart = imagePrefix ? index + 1 : index;
    if (value[linkStart] === '[') {
      const candidate = linkCandidate(value, linkStart);
      if (candidate) {
        const source = imagePrefix ? value.slice(index, candidate.end) : candidate.source;
        flush();
        if (imagePrefix || !safeHttpUrl(candidate.url)) appendPlain(nodes, source);
        else nodes.push(new MarkdownInline('link', '', candidate.url, parseInlineMarkdown(candidate.label), source));
        index = candidate.end;
        continue;
      }
    }
    if (value[index] === '<') {
      const inertHtml = /^<(script|style|iframe|object|embed)\b[^>]*>[\s\S]*?<\/\1\s*>/i.exec(value.slice(index));
      if (inertHtml) {
        plain += inertHtml[0];
        index += inertHtml[0].length;
        continue;
      }
      const close = value.indexOf('>', index + 1);
      if (close > index) {
        const url = value.slice(index + 1, close);
        if (safeHttpUrl(url)) {
          flush();
          nodes.push(new MarkdownInline('link', '', url, [new MarkdownInline('text', url, '', [], url)], value.slice(index, close + 1)));
          index = close + 1;
          continue;
        }
      }
    }
    const bareUrl = bareUrlAt(value, index);
    if (bareUrl) {
      flush();
      nodes.push(new MarkdownInline('link', '', bareUrl, [new MarkdownInline('text', bareUrl, '', [], bareUrl)], bareUrl));
      index += bareUrl.length;
      continue;
    }
    let delimiter = '';
    let kind: MarkdownInlineKind = 'italic';
    if (value.startsWith('**', index) || value.startsWith('__', index)) {
      delimiter = value.slice(index, index + 2); kind = 'bold';
    } else if ((value[index] === '*' || value[index] === '_') && value[index + 1] !== value[index]) {
      delimiter = value[index]; kind = 'italic';
    }
    if (delimiter && !escapedAt(value, index)) {
      const next = value[index + delimiter.length] || '';
      const before = value[index - 1] || '';
      const underscoreInWord = delimiter === '_' && isWordCharacter(before) && isWordCharacter(next);
      const close = next.trim().length > 0 && !underscoreInWord ? emphasisClose(value, index, delimiter) : -1;
      if (close > index + delimiter.length) {
        flush();
        const end = close + delimiter.length;
        const source = value.slice(index, end);
        nodes.push(new MarkdownInline(kind, '', '', parseInlineMarkdown(value.slice(index + delimiter.length, close)), source));
        index = end;
        continue;
      }
      plain += delimiter;
      index += delimiter.length;
      continue;
    }
    plain += value[index];
    index++;
  }
  flush();
  return nodes;
}

class MarkdownParser {
  private lines: string[] = [];
  private index: number = 0;

  constructor(source: string) { this.lines = source.split('\n'); }

  parse(depth: number = 0): MarkdownBlock[] {
    if (depth > 24) {
      const source = this.lines.join('\n');
      return source ? [new MarkdownBlock('raw', source)] : [];
    }
    const blocks: MarkdownBlock[] = [];
    while (this.index < this.lines.length) {
      const line = this.lines[this.index];
      if (!line.trim()) { this.index++; continue; }
      const fence = fenceMarker(line);
      if (fence) { blocks.push(this.readFence(fence)); continue; }
      const heading = headingMatch(line);
      if (heading) {
        const block = new MarkdownBlock('heading', line);
        block.level = heading[2].length;
        block.inlines = parseInlineMarkdown(headingBody(heading[3]));
        blocks.push(block); this.index++; continue;
      }
      if (isRule(line)) { blocks.push(new MarkdownBlock('rule', line)); this.index++; continue; }
      if (quotePrefix(line)) { blocks.push(this.readQuote(depth)); continue; }
      const table = this.readTable();
      if (table) { blocks.push(table); continue; }
      if (listMarker(line)) { blocks.push(this.readList(depth)); continue; }
      blocks.push(this.readParagraph());
    }
    return blocks;
  }

  private readFence(fence: FenceMarker): MarkdownBlock {
    const start = this.index;
    let close = start + 1;
    while (close < this.lines.length && !isFenceClose(this.lines[close], fence)) close++;
    if (close >= this.lines.length) {
      this.index = this.lines.length;
      const source = this.lines.slice(start).join('\n');
      const raw = new MarkdownBlock('raw', source); raw.text = source; return raw;
    }
    const source = this.lines.slice(start, close + 1).join('\n');
    const block = new MarkdownBlock('code', source);
    block.language = fence.language;
    const codeLines = this.lines.slice(start + 1, close);
    block.text = codeLines.length > 0 ? codeLines.join('\n') + '\n' : '';
    this.index = close + 1;
    return block;
  }

  private readQuote(depth: number): MarkdownBlock {
    const start = this.index;
    const quoted: string[] = [];
    while (this.index < this.lines.length) {
      const prefix = quotePrefix(this.lines[this.index]);
      if (!prefix) break;
      quoted.push(this.lines[this.index].slice(prefix[0].length));
      this.index++;
    }
    const block = new MarkdownBlock('quote', this.lines.slice(start, this.index).join('\n'));
    block.children = new MarkdownParser(quoted.join('\n')).parse(depth + 1);
    return block;
  }

  private readList(depth: number): MarkdownBlock {
    const start = this.index;
    const first = listMarker(this.lines[this.index]);
    const block = new MarkdownBlock('list');
    if (!first) return block;
    block.ordered = first.ordered;
    while (this.index < this.lines.length) {
      const marker = listMarker(this.lines[this.index]);
      if (!marker || marker.indent !== first.indent || marker.ordered !== first.ordered) break;
      const itemStart = this.index;
      const itemLines: string[] = [marker.content];
      const item = new MarkdownBlock('list-item');
      item.marker = marker.ordered ? marker.marker : '•';
      this.index++;
      while (this.index < this.lines.length) {
        const nextLine = this.lines[this.index];
        const nextMarker = listMarker(nextLine);
        if (nextMarker && nextMarker.indent === first.indent) break;
        if (!nextLine.trim()) {
          const following = this.index + 1 < this.lines.length ? listMarker(this.lines[this.index + 1]) : undefined;
          if (following && following.indent === first.indent && following.ordered === first.ordered) break;
          if (this.index + 1 < this.lines.length && leadingSpaces(this.lines[this.index + 1]) >= marker.contentIndent) {
            itemLines.push(''); this.index++; continue;
          }
          break;
        }
        const indent = leadingSpaces(nextLine);
        if (indent < marker.contentIndent) break;
        itemLines.push(nextLine.slice(marker.contentIndent));
        this.index++;
      }
      item.source = this.lines.slice(itemStart, this.index).join('\n');
      item.children = new MarkdownParser(itemLines.join('\n')).parse(depth + 1);
      block.items.push(item);
    }
    block.source = this.lines.slice(start, this.index).join('\n');
    return block;
  }

  private readTable(): MarkdownBlock | undefined {
    const start = this.index;
    if (start + 1 >= this.lines.length || !hasTableSeparator(this.lines[start])) return undefined;
    const headerCells = splitTableCells(this.lines[start]);
    const alignments = tableAlignments(this.lines[start + 1], headerCells.length);
    if (!alignments) return undefined;
    const block = new MarkdownBlock('table');
    block.alignments = alignments;
    const header = new MarkdownTableRow();
    header.header = true;
    header.cells = headerCells.map((cell: string) => parseInlineMarkdown(cell));
    block.rows.push(header);
    this.index += 2;
    while (this.index < this.lines.length && this.lines[this.index].trim() && hasTableSeparator(this.lines[this.index])) {
      const cells = splitTableCells(this.lines[this.index]);
      if (cells.length !== headerCells.length) break;
      const row = new MarkdownTableRow();
      row.cells = cells.map((cell: string) => parseInlineMarkdown(cell));
      block.rows.push(row);
      this.index++;
    }
    block.source = this.lines.slice(start, this.index).join('\n');
    return block;
  }

  private readParagraph(): MarkdownBlock {
    const start = this.index;
    this.index++;
    while (this.index < this.lines.length && this.lines[this.index].trim() && !this.isBlockStart(this.index)) this.index++;
    const source = this.lines.slice(start, this.index).join('\n');
    const block = new MarkdownBlock('paragraph', source);
    block.inlines = parseInlineMarkdown(source);
    return block;
  }

  private isBlockStart(index: number): boolean {
    const line = this.lines[index];
    return !!fenceMarker(line) || !!headingMatch(line) || isRule(line) || !!quotePrefix(line) || !!listMarker(line) ||
      (index + 1 < this.lines.length && hasTableSeparator(line) && !!tableAlignments(this.lines[index + 1], splitTableCells(line).length));
  }
}

function leadingSpaces(value: string): number {
  const match = /^ */.exec(value);
  return match ? match[0].length : 0;
}

export function parseMarkdown(source: string): MarkdownBlock[] {
  return new MarkdownParser(source.replace(/\r\n?/g, '\n')).parse();
}

export function markdownInlineText(nodes: MarkdownInline[]): string {
  let result = '';
  for (const node of nodes) {
    if (node.kind === 'text' || node.kind === 'code') result += node.text;
    else result += markdownInlineText(node.children);
  }
  return result;
}

export function markdownVisibleText(blocks: MarkdownBlock[]): string {
  const pieces: string[] = [];
  for (const block of blocks) {
    if (block.kind === 'paragraph' || block.kind === 'heading') pieces.push(markdownInlineText(block.inlines));
    else if (block.kind === 'code' || block.kind === 'raw') pieces.push(block.text);
    else if (block.kind === 'quote') pieces.push(markdownVisibleText(block.children));
    else if (block.kind === 'list') {
      for (const item of block.items) pieces.push(markdownVisibleText(item.children));
    } else if (block.kind === 'table') {
      for (const row of block.rows) pieces.push(row.cells.map((cell: MarkdownInline[]) => markdownInlineText(cell)).join('\t'));
    }
  }
  return pieces.join('\n');
}
