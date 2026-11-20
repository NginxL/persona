// @ someone on Feishu from a text box (the phone's LarkMentions): the `@name` being typed, the draft once a person is
// picked, and which of the picked people the text still @s when it is sent. The text holds each `@name`; `mentions`
// says who that is, as a Feishu chat message does. ui/LarkPeoplePicker.tsx is the menu; where the people come from is
// the caller's (a document's comments: documents/people; a chat: its members). No DOM here.

export interface LarkPerson {
  user_id: string;
  name: string;
  avatar?: string;
  avatar_url?: string;
}

export interface PersonToken {
  /** Offset of the `@`. */
  start: number;
  /** Offset just past the query (the caret). */
  end: number;
  query: string;
}

/**
 * The `@name` being typed just before the caret. Chinese runs straight up to an @ (no space before it in Chinese text), so anything but
 * the characters of an e-mail address may stand before it; the name runs to the caret without spaces.
 */
export function personAt(text: string, caret: number): PersonToken | null {
  const before = text.slice(0, caret);
  const m = /(^|[^A-Za-z0-9._%+-])@([^\s@]{0,24})$/.exec(before);
  if (!m) return null;
  return { start: before.length - m[2].length - 1, end: caret, query: m[2] };
}

/** The draft with the token replaced by `@name ` and where the caret goes (no second space when one follows). */
export function insertPerson(text: string, token: PersonToken, p: Pick<LarkPerson, "name">): { text: string; caret: number } {
  const before = text.slice(0, token.start);
  const after = text.slice(token.end);
  const ins = `@${p.name}${after.startsWith(" ") ? "" : " "}`;
  return { text: before + ins + after, caret: before.length + ins.length + (after.startsWith(" ") ? 1 : 0) };
}

/**
 * The people a message @s: of those picked, the ones whose `@name` the text still holds, each `@name` standing for one
 * (LarkMentions.mentions). Longer names are counted first, so a two-character name is not also matched as its one-character prefix.
 */
export function mentionsIn(text: string, picked: LarkPerson[]): LarkPerson[] {
  const seen = new Set<string>();
  const unique = picked.filter((p) => !seen.has(p.user_id) && Boolean(seen.add(p.user_id)));
  let rest = text;
  const left = new Map<string, number>();
  for (const name of [...new Set(unique.map((p) => p.name))].sort((a, b) => b.length - a.length)) {
    const parts = rest.split(`@${name}`);
    left.set(name, parts.length - 1);
    rest = parts.join("\u0000");
  }
  return unique.filter((p) => {
    const n = left.get(p.name) ?? 0;
    if (n <= 0) return false;
    left.set(p.name, n - 1);
    return true;
  });
}
