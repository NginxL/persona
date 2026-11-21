"""Validate docs/feature-map.md without introducing a second metadata format."""
from pathlib import Path
import re
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
DOC = ROOT / "docs/feature-map.md"
HEADER = [
    "Subsystem", "Capability", "Public entry", "Implementation", "Code",
    "Test evidence", "Deployment", "Known gap", "Next step",
]
REF = re.compile(r"^([A-Za-z0-9_+@./-]+):(\d+)$")
STATUS = {
    3: {"merged"},
    5: {"NOT_RUN", "PASS", "FAIL"},
    6: {"NOT_RUN", "deployed", "N/A"},
}


def cells(line):
    return [part.strip() for part in line.strip().strip("|").split("|")]


def references(text):
    for token in re.findall(r"`([^`]+)`", text):
        match = REF.fullmatch(token)
        if match and "/" in match[1]:
            yield match[1], int(match[2])


def feature_rows(lines):
    rows = []
    number = 0
    while number < len(lines):
        if cells(lines[number]) != HEADER:
            number += 1
            continue
        header = number + 1
        number += 2  # Skip the header and its Markdown delimiter.
        while number < len(lines) and lines[number].startswith("|"):
            rows.append((header, number + 1, cells(lines[number])))
            number += 1
    return rows


def validate(root, doc):
    errors = []
    lines = doc.read_text(encoding="utf-8").splitlines()
    rows = feature_rows(lines)
    if not rows:
        return ["no Feature Map rows found"]
    seen = set()
    for header, number, row in rows:
        where = f"docs/feature-map.md:{number}"
        if len(row) != len(HEADER):
            errors.append(f"{where}: expected {len(HEADER)} columns, found {len(row)}")
            continue
        if any(not value for value in row):
            errors.append(f"{where}: empty required cell")
        key = (row[0], row[1])
        if key in seen:
            errors.append(f"{where}: duplicate feature {key[0]} / {key[1]}")
        seen.add(key)
        for index, allowed in STATUS.items():
            status = row[index].split(" ", 1)[0]
            if status not in allowed:
                errors.append(f"{where}: {HEADER[index]} has invalid status {status!r}")
        for index in (4, 5):
            if not list(references(row[index])):
                errors.append(f"{where}: {HEADER[index]} needs a `file:line` reference")
        if number == header + 2 and len(cells(lines[header])) != len(HEADER):
            errors.append(f"{where}: malformed table delimiter")
    checked = set()
    for number, line in enumerate(lines, 1):
        for path, line_number in references(line):
            if (path, line_number) in checked:
                continue
            checked.add((path, line_number))
            if path.startswith("/") or ".." in Path(path).parts:
                errors.append(f"docs/feature-map.md:{number}: {path} is not repository-relative")
                continue
            source = root / path
            if not source.is_file():
                errors.append(f"docs/feature-map.md:{number}: missing file {path}")
                continue
            source_lines = source.read_text(encoding="utf-8", errors="replace").splitlines()
            if not 1 <= line_number <= len(source_lines):
                errors.append(f"docs/feature-map.md:{number}: {path}:{line_number} is outside 1..{len(source_lines)}")
            elif not source_lines[line_number - 1].strip():
                errors.append(f"docs/feature-map.md:{number}: {path}:{line_number} is blank")
    return errors


class FeatureMapTests(unittest.TestCase):
    def test_repository_feature_map(self):
        self.assertEqual([], validate(ROOT, DOC))

    def test_fixture_checks(self):
        header = "| " + " | ".join(HEADER) + " |\n" + "| " + " | ".join(["---"] * len(HEADER)) + " |\n"
        row = "| Hub | Pairing | `POST /api/v1/pair` | merged — code | `server/pair.go:1` | NOT_RUN — `server/pair_test.go:2` | NOT_RUN — no environment | Gap | Next |\n"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "server").mkdir()
            (root / "server/pair.go").write_text("func Pair() {}\n", encoding="utf-8")
            (root / "server/pair_test.go").write_text("package hub\nfunc TestPair() {}\n", encoding="utf-8")
            doc = root / "feature-map.md"
            doc.write_text(header + row, encoding="utf-8")
            self.assertEqual([], validate(root, doc))
            doc.write_text(header + row.replace("merged —", "unknown —"), encoding="utf-8")
            self.assertTrue(any("invalid status" in error for error in validate(root, doc)))
            doc.write_text(header + row.replace("server/pair.go:1", "server/missing.go:1"), encoding="utf-8")
            self.assertTrue(any("missing file" in error for error in validate(root, doc)))
            doc.write_text(header + row.replace("server/pair_test.go:2", "server/pair_test.go:3"), encoding="utf-8")
            self.assertTrue(any("outside 1..2" in error for error in validate(root, doc)))
            doc.write_text(header + row.replace("server/pair.go:1", "/tmp/pair.go:1"), encoding="utf-8")
            self.assertTrue(any("not repository-relative" in error for error in validate(root, doc)))


if __name__ == "__main__":
    unittest.main()
