#!/usr/bin/env python3
"""Fail if a public personad binary carries a skill Persona may not redistribute.

The built-in skills a binary embeds (server/internal/assistantskills/builtin/<name>/SKILL.md) must each be a key of
assistantskills.Redistributable; a copied skill belongs in the user's own space (~/.persona/skills) or comes with its
extension, never in the download. Every SKILL.md frontmatter found in the binary is checked, and the binary must
carry at least one (else the scan itself is broken and would pass anything).

  python3 scripts/release/check_bundle_skills.py path/to/personad-linux-amd64
"""
import re
import sys
from pathlib import Path

EMBED_GO = Path(__file__).resolve().parents[2] / "server" / "internal" / "assistantskills" / "embed.go"
# A SKILL.md as embedded: a frontmatter block (up to 30 lines) with a name line in it, quoted or not, wherever it is,
# and a description line in the same block.
FRONTMATTER = re.compile(rb"---\r?\n((?:(?!---)[^\n]*\n){1,30}?)---")
NAME = re.compile(rb"(?m)^name:[ \t]*[\"']?([A-Za-z0-9_.-]+)[\"']?[ \t]*\r?$")
DESCRIPTION = re.compile(rb"(?m)^description:")


def redistributable(source: str) -> set:
    """The keys of `var Redistributable = map[string]string{…}` in embed.go.

    A key may be a quoted string or a simple constant declared elsewhere in the same file, such as workGuideName.
    Resolving that constant must not accidentally accept an unregistered skill.
    """
    m = re.search(r"var Redistributable = map\[string\]string\{(.*?)\n\}", source, re.S)
    if not m:
        raise SystemExit(f"{EMBED_GO}: no Redistributable map")
    keys = set(re.findall(r'^\s*"([^"]+)"\s*:', m.group(1), re.M))
    for key in re.findall(r'^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:', m.group(1), re.M):
        declaration = re.search(r'^\s*(?:const\s+)?\(?\s*' + re.escape(key) + r'\s*=\s*"([^"]+)"\s*(?://.*)?$', source, re.M)
        if declaration:
            keys.add(declaration.group(1))
    return keys


def check(binary: bytes, allowed: set) -> list:
    names = sorted({m.group(1).decode() for block in FRONTMATTER.findall(binary) if DESCRIPTION.search(block)
                    for m in [NAME.search(block)] if m})
    if not names:
        return ["no embedded skill found: the scan is broken or the built-in skills are gone"]
    return [f"skill {n!r} is in the public build but not in assistantskills.Redistributable" for n in names
            if n not in allowed]


def main(argv: list) -> int:
    if len(argv) != 2:
        print(__doc__.strip().splitlines()[-1].strip(), file=sys.stderr)
        return 2
    problems = check(Path(argv[1]).read_bytes(), redistributable(EMBED_GO.read_text()))
    for p in problems:
        print(f"{argv[1]}: {p}", file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
