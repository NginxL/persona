// A row of pills the keyboard moves along as one stop (a radio group): the Inbox's section bar. The Team panel's worker
// tabs keep their own copy (TeamPanel.tsx nextTab): imported from both, this became a chunk of its own, and every lazy
// page's preload list in the first screen's App chunk moved with it.

/** The pill an arrow key (or Home / End) moves to from `i` among `n`; undefined for any other key. */
export function nextTab(i: number, key: string, n: number): number | undefined {
  if (key === "ArrowRight") return (i + 1) % n;
  if (key === "ArrowLeft") return (i - 1 + n) % n;
  if (key === "Home") return 0;
  if (key === "End") return n - 1;
  return undefined;
}
