// Myers' line diff, on its own so a work group's edit rows (views/chat/ActionRow.tsx, on every first screen) can show
// +N -N without lib/compare.ts's document readers and their strings, which only the Git panel's Compare view uses.
// compare.ts re-exports both names; tested in compare.test.ts.

export type Op = { kind: "same" | "add" | "del"; text: string };

/**
 * Myers' line diff (the one git uses). Inputs past `limit` lines in all, or with more than `maxEdits` changes, come
 * back as null: too much to show as a diff.
 */
export function diffLines(a: string[], b: string[], limit = 60_000, maxEdits = 6_000): Op[] | null {
  const n = a.length;
  const m = b.length;
  if (n + m > limit) return null;
  const max = Math.min(n + m, maxEdits);
  const off = max + 1;
  const v = new Int32Array(2 * max + 3);
  const trace: Int32Array[] = [];
  let found = -1;
  for (let d = 0; d <= max; d++) {
    trace.push(v.slice());
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[off + k - 1] < v[off + k + 1]) ? v[off + k + 1] : v[off + k - 1] + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x++;
        y++;
      }
      v[off + k] = x;
      if (x >= n && y >= m) {
        found = d;
        break;
      }
    }
    if (found >= 0) break;
  }
  if (found < 0) return null;
  // walk back through the saved frontiers
  const ops: Op[] = [];
  let x = n;
  let y = m;
  for (let d = found; d > 0; d--) {
    const pv = trace[d];
    const k = x - y;
    const prevK = k === -d || (k !== d && pv[off + k - 1] < pv[off + k + 1]) ? k + 1 : k - 1;
    const prevX = pv[off + prevK];
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) ops.push({ kind: "same", text: a[--x] }), y--;
    if (x === prevX) ops.push({ kind: "add", text: b[--y] });
    else ops.push({ kind: "del", text: a[--x] });
  }
  while (x > 0 && y > 0) ops.push({ kind: "same", text: a[--x] }), y--;
  return ops.reverse();
}
