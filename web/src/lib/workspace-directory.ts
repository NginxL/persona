/** Match the hub's CanonicalCwd for remote POSIX, Windows drive and UNC syntax. */
export function directory(p: string): string | null {
  if (!p || p.includes("\0")) return null;
  const clean = (path: string) => {
    const parts: string[] = [];
    for (const part of path.split("/")) {
      if (!part || part === ".") continue;
      if (part === "..") parts.pop(); else parts.push(part);
    }
    return `/${parts.join("/")}`;
  };
  if (/^[a-z]:/i.test(p)) return /^[a-z]:[\\/]/i.test(p) ? p.slice(0, 2).toLowerCase() + clean(p.slice(2).replaceAll("\\", "/")) : null;
  if (p.startsWith("\\\\") || p.startsWith("//")) {
    const parts = p.slice(2).replaceAll("\\", "/").split("/");
    if (parts.length < 2 || parts.slice(0, 2).some((v) => !v || v === "." || v === "..")) return null;
    const root = `//${parts[0].toLowerCase()}/${parts[1].toLowerCase()}`;
    const rest = clean(parts.slice(2).join("/"));
    return root + (rest === "/" ? "" : rest);
  }
  return p.startsWith("/") ? clean(p) : null;
}

/** Comparison identity is case-insensitive on Windows; directory() keeps the path spelling used for display. */
export function directoryKey(p: string): string | null {
  const canonical = directory(p);
  return canonical !== null && /^(?:[a-z]:\/|\/\/)/i.test(canonical) ? canonical.toLowerCase() : canonical;
}

export function sameDirectory(a: string, b: string): boolean {
  const canonical = directoryKey(a);
  return canonical !== null && canonical === directoryKey(b);
}
