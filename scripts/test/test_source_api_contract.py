"""Issue #1185: the maintained source API handler design is a reviewable contract.

    python3 scripts/test/test_source_api_contract.py

This is a documentation conformance suite, not an implementation or activation
hook. It requires typed declarations, explicit authorization and durable-write
rules, bounded tables, independent subcontracts and a traceable downstream
acceptance matrix. AC9's final diff file-list inspection deliberately remains a
review/CI responsibility: deriving a PR base in this test would be brittle.
"""

import json
from pathlib import Path
import re
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
CONTRACT = ROOT / "docs/extensions/source-api-handlers.md"
DOC = "docs/extensions/source-api-handlers.md"

FENCE = re.compile(r"^[ \t]*(`{3,}|~{3,})(.*)$")
HEADING = re.compile(r"^(#{1,6})\s+(.+?)\s*#*\s*$")
PLACEHOLDER = re.compile(r"^(?:-|tbd|todo|n a|none|unlimited|unbounded|implementation defined)$")
BYTE_UNIT = r"(?:bytes?|kib|mib|gib|kb|mb|gb)"
TIME_UNIT = r"(?:seconds?|milliseconds?|microseconds?|minutes?|ms|s)"

# Section matching intentionally uses concepts rather than complete headings.
REQUIRED_SECTIONS = {
    "declarations and ownership": (r"declaration",),
    "authentication and caller binding": (r"authentication|caller binding",),
    "assistant command gates": (r"assistant", r"command"),
    "budgets and cancellation": (r"budget",),
    "media semantics": (r"media",),
    "durable writes": (r"durable|confirmation", r"write|state|confirmation"),
    "managed credential source": (r"credential", r"managed|companion|provider|source"),
    "sensitive session API": (r"session", r"sensitive|rename|mode"),
    "isolation and audit": (r"isolation|audit",),
    "implementation acceptance": (r"acceptance", r"implementation|matrix|plan"),
    "phase boundary": (r"phase|scope", r"boundary|design"),
}


def normalized(text):
    """Make wording, Markdown punctuation and identifier style insignificant."""
    return re.sub(r"\s+", " ", re.sub(r"[^a-z0-9]+", " ", text.casefold())).strip()


def visible_lines(text):
    """Return numbered lines outside fenced code, using CommonMark fence rules."""
    fence = ""
    for number, line in enumerate(text.splitlines(), 1):
        mark = FENCE.match(line)
        if not fence:
            if mark and not (mark[1][0] == "`" and "`" in mark[2]):
                fence = mark[1]
            else:
                yield number, line
        elif mark and mark[1][0] == fence[0] and len(mark[1]) >= len(fence) and not mark[2].strip():
            fence = ""


def markdown_sections(text):
    """Return headings with their bodies, including nested headings and fences."""
    lines = text.splitlines()
    visible = dict(visible_lines(text))
    headings = []
    for number, line in visible.items():
        match = HEADING.match(line)
        if match:
            headings.append((number, len(match[1]), match[2]))
    result = []
    for index, (number, level, title) in enumerate(headings):
        end = len(lines) + 1
        for later_number, later_level, _ in headings[index + 1:]:
            if later_level <= level:
                end = later_number
                break
        result.append({"title": title, "level": level,
                       "text": "\n".join(lines[number - 1:end - 1])})
    return result


def table_cells(line):
    line = line.strip()
    if line.startswith("|"):
        line = line[1:]
    if line.endswith("|") and not line.endswith(r"\|"):
        line = line[:-1]
    return [cell.strip().replace(r"\|", "|") for cell in re.split(r"(?<!\\)\|", line)]


def delimiter_row(cells):
    return bool(cells) and all(re.fullmatch(r":?-{3,}:?", cell.replace(" ", "")) for cell in cells)


def markdown_tables(text):
    """Parse pipe tables outside fences; each result has headers, rows and a line."""
    lines = text.splitlines()
    visible = dict(visible_lines(text))
    tables = []
    for number, line in visible.items():
        if not line.strip().startswith("|") or number + 1 not in visible:
            continue
        headers = table_cells(line)
        delimiter = table_cells(visible[number + 1])
        if len(headers) != len(delimiter) or not delimiter_row(delimiter):
            continue
        rows = []
        following = number + 2
        while following in visible and visible[following].strip().startswith("|"):
            row = table_cells(visible[following])
            row += [""] * (len(headers) - len(row))
            rows.append(row)
            following += 1
        tables.append({"line": number, "headers": headers, "rows": rows})
    return tables


def fenced_blocks(text):
    """Yield (info-string language, body) for every fenced block."""
    blocks = []
    lines = text.splitlines()
    index = 0
    while index < len(lines):
        mark = FENCE.match(lines[index])
        if not mark or (mark[1][0] == "`" and "`" in mark[2]):
            index += 1
            continue
        opening = mark[1]
        language = mark[2].strip().split(maxsplit=1)[0].lower() if mark[2].strip() else ""
        body = []
        index += 1
        while index < len(lines):
            closing = FENCE.match(lines[index])
            if closing and closing[1][0] == opening[0] and len(closing[1]) >= len(opening) and not closing[2].strip():
                index += 1
                break
            body.append(lines[index])
            index += 1
        blocks.append((language, "\n".join(body)))
    return blocks


def column(table, pattern):
    for index, header in enumerate(table["headers"]):
        if re.search(pattern, normalized(header)):
            return index
    return None


def json_schema_artifacts(node):
    """Find JSON-Schema property maps without requiring a particular $id/title."""
    artifacts = []
    if isinstance(node, dict):
        properties = node.get("properties")
        if isinstance(properties, dict):
            fields = {}
            for name, spec in properties.items():
                if not isinstance(spec, dict):
                    continue
                if "$ref" in spec:
                    fields[name] = "$ref"
                elif "const" in spec:
                    fields[name] = "const"
                elif "enum" in spec:
                    fields[name] = "enum"
                elif any(key in spec for key in ("oneOf", "anyOf", "allOf")):
                    fields[name] = "schema union"
                elif "type" in spec:
                    fields[name] = "inline schema" if spec.get("type") == "object" and "properties" in spec else str(spec["type"])
            if fields:
                artifacts.append(fields)
        for value in node.values():
            artifacts.extend(json_schema_artifacts(value))
    elif isinstance(node, list):
        for value in node:
            artifacts.extend(json_schema_artifacts(value))
    return artifacts


def declaration_artifacts(text):
    """Return field->declared-type maps from field tables, TypeScript or JSON Schema."""
    artifacts = []
    for table in markdown_tables(text):
        name_column = column(table, r"^(?:field|property|name)$")
        type_column = column(table, r"^(?:type|declared type|schema type)$")
        if name_column is None or type_column is None:
            continue
        fields = {row[name_column]: row[type_column] for row in table["rows"] if row[name_column].strip()}
        if fields:
            artifacts.append(fields)
    property_pattern = re.compile(
        r"^\s*(?:readonly\s+)?[\"']?([A-Za-z_][\w-]*)[\"']?\s*\??\s*:\s*([^;,\n]+)", re.M)
    for language, body in fenced_blocks(text):
        if language in ("ts", "typescript"):
            fields = dict(property_pattern.findall(body))
            if fields:
                artifacts.append(fields)
        elif language == "json":
            try:
                artifacts.extend(json_schema_artifacts(json.loads(body)))
            except json.JSONDecodeError:
                pass
    return artifacts


DECLARATION_FIELDS = {
    "version": r"\b(?:declaration |contract |api )?version\b",
    "method": r"\bmethod\b",
    "path": r"\bpath\b",
    "request schema": r"\brequest(?: body)?(?: schema)?\b",
    "response schema": r"\bresponse(?: body)?(?: schema)?\b",
    "callers": r"\bcallers\b",
    "reconciliation schema": r"\breconciliation schema\b",
    "query only": r"\bquery only\b",
    "read justification": r"\bread justification\b",
}


def complete_declaration(fields):
    matched = {}
    for requirement, pattern in DECLARATION_FIELDS.items():
        for name, declared_type in fields.items():
            type_text = normalized(declared_type)
            if not re.search(pattern, normalized(name)) or PLACEHOLDER.fullmatch(type_text):
                continue
            if type_text in ("any", "unknown", "untyped", "object"):
                continue
            if requirement.endswith("schema") and not re.search(r"schema|ref|request|response", type_text):
                continue
            matched[requirement] = (name, declared_type)
            break
    return matched


def bounded_limit(cell):
    value = normalized(cell)
    return bool(value and not PLACEHOLDER.fullmatch(value)
                and (re.search(r"\d", value) or re.search(r"\bnegotiated\b", value)))


class ContractPresenceTests(unittest.TestCase):
    def test_contract_document_exists(self):
        self.assertTrue(CONTRACT.is_file(),
                        f"{DOC} is absent; issue #1185 requires the maintained design contract before implementation")


@unittest.skipUnless(CONTRACT.is_file(), f"{DOC} is absent; ContractPresenceTests reports the red precondition")
class SourceApiContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.document = CONTRACT.read_text(encoding="utf-8")
        cls.sections = markdown_sections(cls.document)

    def section(self, name):
        patterns = REQUIRED_SECTIONS[name]
        matches = [section for section in self.sections
                   if all(re.search(pattern, normalized(section["title"])) for pattern in patterns)]
        self.assertTrue(matches, f"{DOC} needs a section for {name}; heading concepts: {patterns}")
        return matches[0]["text"]

    def assert_concepts(self, text, concepts):
        content = normalized(text)
        for label, pattern in concepts.items():
            with self.subTest(requirement=label):
                self.assertTrue(re.search(pattern, content),
                                f"missing or insufficiently explicit {label}; expected concept /{pattern}/")

    def assert_normative(self, text, name):
        self.assertRegex(text, r"(?i)\b(?:MUST|SHALL|REQUIRED)(?:\s+NOT)?\b",
                         f"the {name} section must state normative MUST/SHALL/REQUIRED rules")

    def test_required_normative_section_inventory(self):
        for name in REQUIRED_SECTIONS:
            with self.subTest(section=name):
                text = self.section(name)
                if name != "implementation acceptance":
                    self.assert_normative(text, name)

    def test_typed_versioned_declarations_and_route_ownership(self):
        text = self.section("declarations and ownership")
        artifacts = declaration_artifacts(text)
        complete = [complete_declaration(fields) for fields in artifacts]
        self.assertTrue(any(len(fields) == len(DECLARATION_FIELDS) for fields in complete),
                        "a declaration table, TypeScript declaration or JSON Schema must give typed version, exact "
                        "method/path and request/response schema fields; generic any/object schemas do not count")
        self.assert_concepts(text, {
            "an explicit declaration version": r"\bversion\b.{0,60}\b\d+\b|\bv\d+\b",
            "request/response schema allowlists": r"\b(?:allowlist|allow list|whitelist)\b",
            "exact route matching": r"\bexact\b",
            "normalized paths and bypass prevention": r"\bnormaliz\w*\b.{0,100}\b(?:bypass|canonical|collision|dot segment|encoded)\b|\b(?:bypass|canonical|collision|dot segment|encoded)\b.{0,100}\bnormaliz\w*\b",
            "decoded segments are revalidated": r"\brevalidat\w*\b.{0,80}\bdecoded segment\b|\bdecoded segment\b.{0,80}\brevalidat\w*\b",
            "route collision/overlap rules": r"\b(?:collision|overlap)\b",
            "manager/hub reserved routes": r"\b(?:reserved|manager owned|hub owned)\b",
            "undeclared methods": r"\bundeclared\b.{0,60}\b(?:method|route|path)\b|\b(?:method|route|path)\b.{0,60}\bundeclared\b",
            "wildcard/prefix rejection": r"\b(?:wildcard|glob|prefix)\b.{0,80}\b(?:reject|forbid|not|never)\b|\b(?:reject|forbid|not|never)\b.{0,80}\b(?:wildcard|glob|prefix)\b",
            "manager ownership": r"\bmanager\b",
            "manager admission": r"\badmission\b",
            "manager dispatch": r"\bdispatch\b",
            "manager lifecycle": r"\blifecycle\b",
            "nonempty closed caller classes": r"\bnonempty\b.{0,80}\bcallers\b|\bcallers\b.{0,80}\bnonempty\b",
            "GET and HEAD are read-only": r"\bget\b.{0,50}\bhead\b.{0,100}\bread\b|\bread\b.{0,100}\bget\b.{0,50}\bhead\b",
            "PUT PATCH and DELETE cannot be reads": r"\bput\b.{0,50}\bpatch\b.{0,50}\bdelete\b.{0,100}\beffect write\b|\bread declarations\b.{0,160}\bput\b",
            "POST reads require query-only justification": r"\bquery only\b.{0,100}\bread justification\b|\bread justification\b.{0,100}\bquery only\b",
            "write reconciliation schema is required": r"\bwrite\b.{0,120}\breconciliation schema\b|\breconciliation schema\b.{0,80}\brequired\b",
            "hub is an authenticated relay": r"\bhub\b.{0,80}\bauthenticat\w*\b.{0,80}\brelay\b|\bauthenticat\w*\b.{0,80}\brelay\b.{0,80}\bhub\b",
            "no source listener/socket/generic-proxy capability": r"\b(?:no|never|not|forbid|forbidden|reject)\b.{0,160}\blistener\b.{0,160}\bunix socket\b.{0,160}\bgeneric proxy\b|\blistener\b.{0,160}\bunix socket\b.{0,160}\bgeneric proxy\b.{0,160}\b(?:forbidden|not allowed|rejected)\b",
        })

    def test_authentication_caller_binding_and_separate_grants(self):
        text = self.section("authentication and caller binding")
        self.assert_concepts(text, {
            "authentication before dispatch": r"\bauthenticat\w*\b.{0,80}\b(?:before|prior to)\b.{0,80}\bdispatch\b|\bdispatch\b.{0,80}\bonly after\b.{0,80}\bauthenticat\w*\b",
            "app admission": r"\bapp\b",
            "assistant admission": r"\bassistant\b",
            "extension-to-extension admission": r"\bextension to extension\b",
            "independent authorization rules": r"\b(?:separate|distinct|independent)\b",
            "explicit grants": r"\b(?:grant|permission)\b",
            "verified caller": r"\bcaller\b",
            "installation binding": r"\binstallation\b",
            "contribution binding": r"\bcontribution\b",
            "account binding where applicable": r"\baccount\b",
            "process-generation binding": r"\b(?:process generation|generation)\b",
            "request-ID binding": r"\brequest id\b",
            "caller-visible idempotency key": r"\bidempotency key\b",
            "request identity includes operation and method": r"\broute command and exact http method\b",
            "embedded replay expiry": r"\bnot after\b",
            "replay requires a current grant": r"\bcurrent grant\b",
            "expired replay is rejected": r"\brequest expired\b",
            "host-derived context": r"\b(?:host derived|host provided|verified host)\b",
            "forged identity-header stripping": r"\b(?:strip|remove|drop)\w*\b.{0,80}\bforg\w*\b.{0,80}\bheader\b|\bforg\w*\b.{0,80}\bheader\b.{0,80}\b(?:strip|remove|drop)\w*\b",
            "schema-allowlisted headers survive": r"\ballowlisted\b.{0,100}\bsurvive\b|\bsurvive\b.{0,100}\ballowlisted\b",
            "no bearer credential forwarding": r"\bbearer\b.{0,100}\b(?:never|not|no)\b|\b(?:never|not|no)\b.{0,100}\bbearer\b",
            "a handle is not authorization": r"\bhandle\b.{0,100}\b(?:not|never|no)\b.{0,80}\bauthoriz|\bauthoriz\w*\b.{0,100}\bhandle\b.{0,80}\b(?:not|never|no)\b",
        })

    def test_assistant_command_mapping_and_write_gates(self):
        text = self.section("assistant command gates")
        candidates = []
        for table in markdown_tables(text):
            indices = {
                "command": column(table, r"command"),
                "handler": column(table, r"handler"),
                "schema": column(table, r"schema|argument|body"),
                "grant": column(table, r"grant|permission"),
            }
            if all(index is not None for index in indices.values()):
                candidates.append((table, indices))
        self.assertTrue(candidates, "the bounded command-to-handler mapping needs command, handler, argument/body "
                                    "schema and grant/permission columns")
        table, indices = candidates[0]
        self.assertTrue(table["rows"], "the command allowlist mapping must not be empty")
        for row in table["rows"]:
            for name, index in indices.items():
                self.assertTrue(row[index].strip(), f"command mapping row has no {name}")
            self.assertNotIn(normalized(row[indices["schema"]]), ("any", "unknown", "arbitrary", "generic"))
        self.assert_concepts(text, {
            "preserved existing command allowlist": r"\b(?:existing|current|preserv\w*|retain\w*)\b.{0,80}\ballowlist\b|\ballowlist\b.{0,80}\b(?:existing|current|preserv\w*|retain\w*)\b",
            "bounded typed mapping": r"\b(?:bounded|closed|typed)\b",
            "argument validation": r"\bargument\b.{0,80}\bvalidat|\bvalidat\w*\b.{0,80}\bargument\b",
            "body validation": r"\bbody\b.{0,80}\bvalidat|\bvalidat\w*\b.{0,80}\bbody\b",
            "caller attribution": r"\bcaller\b.{0,60}\battribut|\battribut\w*\b.{0,60}\bcaller\b",
            "per-write confirmation": r"\b(?:per write|each write|write)\b.{0,80}\bconfirmation\b|\bconfirmation\b.{0,80}\bwrite\b",
            "no shell or arbitrary forwarding": r"\b(?:no|not|never|forbid|forbidden|reject)\b.{0,140}\bshell\b.{0,140}\barbitrary\b|\bshell\b.{0,140}\barbitrary\b.{0,140}\b(?:forbidden|not allowed|rejected)\b",
            "no generic hub endpoint": r"\bgeneric hub\b.{0,80}\b(?:forbidden|not allowed|rejected|no)\b|\b(?:no|not|never|forbid|forbidden|reject)\b.{0,80}\bgeneric hub\b",
            "credential references and command permissions are separate grants": r"\bcredential\b.{0,100}\b(?:reference|grant)\b.{0,100}\b(?:separate|distinct|independent)\b.{0,100}\b(?:command|permission|grant)\b|\bcommand\b.{0,100}\b(?:separate|distinct|independent)\b.{0,100}\bcredential\b",
        })

    def test_budget_table_units_ceilings_overload_and_cancellation(self):
        text = self.section("budgets and cancellation")
        candidates = []
        for table in markdown_tables(text):
            resource = column(table, r"resource|dimension|budget|item")
            limit = column(table, r"limit|ceiling|bound|maximum|max")
            unit = column(table, r"^unit|units")
            if resource is not None and limit is not None and unit is not None:
                candidates.append((table, resource, limit, unit))
        self.assertTrue(candidates, "budgets need a Markdown table with resource, ceiling/limit and unit columns")
        table, resource, limit, unit = candidates[0]
        requirements = [
            ("headers", r"\bheaders?\b", BYTE_UNIT + r"|count|headers?"),
            ("URL", r"\burl\b", BYTE_UNIT + r"|characters?"),
            ("query", r"\bquery\b", BYTE_UNIT + r"|characters?"),
            ("request body", r"\brequest\b.{0,40}\b(?:body|bytes)\b", BYTE_UNIT),
            ("response body", r"\bresponse\b.{0,40}\b(?:body|bytes)\b", BYTE_UNIT),
            ("encoded RPC frames", r"\brpc\b.{0,50}\bframe\b|\bframe\b.{0,50}\brpc\b", BYTE_UNIT),
            ("concurrent requests", r"\bconcurr\w*|in flight\b", r"requests?|operations?|count"),
            ("queued work", r"\bqueue\w*\b", BYTE_UNIT + r"|requests?|operations?|items?|count"),
            ("CPU", r"\bcpu\b", r"cpu|" + TIME_UNIT),
            ("time/deadline", r"\b(?:time|deadline|duration|wall clock)\b", TIME_UNIT),
            ("aggregate per-installation work", r"\bper installation\b|\b(?:aggregate|total)\b.{0,60}\binstallation\b|\binstallation\b.{0,60}\b(?:aggregate|total)\b",
             r"cpu|work|requests?|operations?|" + BYTE_UNIT + r"|" + TIME_UNIT + r"|count"),
        ]
        for label, row_pattern, unit_pattern in requirements:
            with self.subTest(budget=label):
                matching = [row for row in table["rows"] if re.search(row_pattern, normalized(row[resource]))]
                self.assertTrue(matching, f"no budget-table row for {label}")
                self.assertTrue(any(bounded_limit(row[limit]) and re.search(rf"\b(?:{unit_pattern})\b", normalized(row[unit]))
                                    for row in matching),
                                f"{label} needs a numeric or explicitly negotiated ceiling and a suitable unit")
        self.assert_concepts(text, {
            "encoded frame accounting": r"\bencoded\b.{0,60}\bframe\b|\bframe\b.{0,60}\bencoded\b",
            "overload result": r"\boverload\w*|too many|resource exhausted\b",
            "typed/error overload result": r"\berror\b.{0,80}\b(?:code|result|status|response)\b|\b(?:code|result|status|response)\b.{0,80}\berror\b",
            "disconnect propagation": r"\bdisconnect\b",
            "timeout propagation": r"\btimeout\b",
            "cancellation propagation": r"\bcancel\w*\b",
            "disable propagation": r"\bdisable\b",
            "uninstall propagation": r"\buninstall\b",
            "generation replacement": r"\bgeneration\b.{0,60}\b(?:replac\w*|supersed\w*)\b|\b(?:replac\w*|supersed\w*)\b.{0,60}\bgeneration\b",
            "late-result rejection": r"\blate\b.{0,60}\b(?:result|callback)\b.{0,80}\b(?:reject|drop|discard|ignore)\b|\b(?:reject|drop|discard|ignore)\w*\b.{0,80}\blate\b.{0,60}\b(?:result|callback)\b",
            "unknown releases live occupancy": r"\bunknown\b.{0,100}\brelease\b|\brelease\b.{0,100}\bunknown\b",
            "existing frame limits are not silently raised": r"\bframe\b.{0,100}\b(?:unchanged|not.{0,40}increas|cannot.{0,40}increas|never.{0,40}increas)\b|\b(?:unchanged|not.{0,40}increas|cannot.{0,40}increas|never.{0,40}increas)\b.{0,100}\bframe\b",
        })

    def test_media_streaming_control_data_planes_and_outbound_enforcement(self):
        text = self.section("media semantics")
        self.assert_concepts(text, {
            "bounded host/source planes": r"\bbounded\b|\bbound\w*\b",
            "host and source": r"\bhost\b.{0,100}\bsource\b|\bsource\b.{0,100}\bhost\b",
            "control plane": r"\bcontrol plane\b",
            "data plane": r"\bdata plane\b",
            "per-chunk budget": r"\bper chunk\b|\bchunk\b.{0,60}\bbudget\b",
            "aggregate media budget": r"\baggregate\b.{0,60}\bbudget\b|\bbudget\b.{0,60}\baggregate\b",
            "backpressure": r"\bbackpressure\b",
            "GET": r"\bget\b",
            "HEAD": r"\bhead\b",
            "Range": r"\brange\b",
            "206": r"\b206\b",
            "416": r"\b416\b",
            "malformed and multiple ranges return 400": r"\bmalformed\b.{0,60}\bmultiple ranges\b.{0,60}\b400\b",
            "oversized source response uses 502": r"\b502\b.{0,80}\bresponse too large\b|\bresponse too large\b.{0,80}\b502\b",
            "content type": r"\bcontent type\b",
            "content length": r"\bcontent length\b",
            "cache validators": r"\bcache validator\b|\betag\b|\blast modified\b",
            "partial-response cancellation": r"\bpartial\b.{0,80}\bcancel\w*\b|\bcancel\w*\b.{0,80}\bpartial\b",
            "approved origins": r"\bapproved origin\b|\borigin allowlist\b",
            "credential injection": r"\bcredential injection\b|\binject\w*\b.{0,50}\bcredential\b",
            "redirect handling": r"\bredirect\b",
            "every outbound read": r"\b(?:every|each|all)\b.{0,60}\boutbound read\b",
            "protected-target enforcement": r"\bprotected target\b",
            "checked DNS": r"\bchecked dns\b|\bdns\b.{0,50}\bcheck\w*\b",
            "TLS enforcement": r"\btls\b",
            "neither buffering nor truncation is parity": r"\bwhole body buffering\b.{0,140}\btruncat\w*\b.{0,100}\b(?:not|neither|never)\b.{0,60}\bparity\b|\b(?:not|neither|never)\b.{0,140}\bwhole body buffering\b.{0,140}\btruncat\w*\b.{0,100}\bparity\b",
        })

    def test_durable_confirmation_and_reservation_state_machine(self):
        text = self.section("durable writes")
        state_tables = []
        for table in markdown_tables(text):
            current = column(table, r"current state|from state|^state$|^from$")
            event = column(table, r"event|condition|trigger|transition")
            following = column(table, r"next state|to state|^to$|transition")
            if current is not None and event is not None and following is not None:
                state_tables.append(table)
        self.assertTrue(state_tables, "the durable reservation/receipt state machine needs a transition table")
        self.assertGreaterEqual(sum(len(table["rows"]) for table in state_tables), 3,
                                "the state machine needs reservation, outcome and recovery transitions")
        transitions = normalized("\n".join(" ".join(" ".join(row) for row in table["rows"]) for table in state_tables))
        for label, pattern in {
            "reserved state": r"\breserv\w*\b",
            "committed state": r"\bcommit\w*\b",
            "unknown/ambiguous state": r"\b(?:unknown|ambiguous|indeterminate)\b",
            "reconciliation/recovery transition": r"\b(?:reconcil\w*|recover\w*)\b",
        }.items():
            with self.subTest(state_machine=label):
                self.assertRegex(transitions, pattern)
        self.assert_concepts(text, {
            "trusted-owner verification": r"\btrusted owner\b|\bowner\b.{0,60}\bverif\w*\b",
            "operation-specific confirmation": r"\boperation specific\b.{0,60}\bconfirmation\b|\bconfirmation\b.{0,60}\boperation specific\b",
            "caller binding": r"\bcaller\b",
            "target binding": r"\btarget\b",
            "payload fingerprint": r"\bpayload fingerprint\b|\bfingerprint\b.{0,40}\bpayload\b",
            "fingerprint includes operation and method": r"\bfingerprint\b.{0,100}\broute command and http method\b|\broute command and http method\b.{0,100}\bfingerprint\b",
            "only post-dispatch cancellation is unknown": r"\bafter the atomic dispatched transition\b",
            "request ID": r"\brequest id\b",
            "expiry": r"\bexpir\w*\b",
            "durable reservation": r"\breservation\b",
            "durable receipt": r"\breceipt\b",
            "persist before external effects": r"\b(?:persist|save|write)\w*\b.{0,100}\b(?:before|prior to)\b.{0,100}\bexternal effect\b|\bexternal effect\b.{0,100}\bonly after\b.{0,100}\b(?:persist|save|write)\w*\b",
            "defined commit point": r"\bcommit point\b",
            "reservation-save failure has no external effect": r"\breservation\b.{0,100}\b(?:save|persist|write)\b.{0,100}\bfail\w*\b.{0,120}\bno external effect\b|\bfail\w*\b.{0,100}\breservation\b.{0,120}\bno external effect\b",
            "ambiguous outcome": r"\b(?:ambiguous|unknown|indeterminate)\b",
            "no new-ID replay": r"\bnew id\b.{0,100}\b(?:not|never|no|forbid)\b.{0,80}\b(?:retr\w*|replay)\b|\b(?:not|never|no|forbid)\b.{0,100}\b(?:retr\w*|replay)\b.{0,100}\bnew id\b|\bnew id\b.{0,100}\bunsafe\b",
            "no duplicate writes": r"\bduplicate write\b|\bduplicate\b.{0,50}\bexternal effect\b",
            "bounded receipt retention": r"\bbounded\b.{0,80}\b(?:receipt|retention)\b|\b(?:receipt|retention)\b.{0,80}\bbounded\b",
            "expired replay is rejected without a tombstone": r"\brequest expired\b.{0,100}\btombstone\b|\btombstone\b.{0,100}\brequest expired\b",
            "redacted receipts": r"\bredact\w*\b.{0,60}\breceipt\b|\breceipt\b.{0,60}\bredact\w*\b",
            "real Postgres restart recovery": r"\breal postgres\b.{0,100}\brestart\b|\bpostgres\b.{0,100}\brestart\b",
            "manager restart recovery": r"\bmanager\b.{0,100}\brestart\b|\brestart\b.{0,100}\bmanager\b",
            "recovery semantics": r"\brecover\w*|reconcil\w*\b",
            "extension durable-state coordination": r"\bextension\b.{0,80}\bdurable state\b|\bdurable state\b.{0,80}\bextension\b",
            "memory is not durable": r"\bmemory\b.{0,80}\bnot durable\b|\bnot\b.{0,60}\bmemory\b.{0,60}\bdurable\b|\bmemory\b.{0,80}\bnever\b.{0,40}\bdurable\b",
        })

    def test_managed_credential_subcontract_is_independent(self):
        text = self.section("managed credential source")
        self.assert_concepts(text, {
            "separate explicit grant": r"\bexplicit grant\b|\bseparate\b.{0,60}\bgrant\b",
            "reviewed schema": r"\breviewed schema\b|\bschema\b.{0,60}\breview\w*\b",
            "no inherited core authority": r"\b(?:not|never|no)\b.{0,100}\binherit\w*\b.{0,100}\bcore\b|\bcore\b.{0,100}\b(?:not|never|no)\b.{0,80}\bauthoriz|\bindependent\b.{0,80}\bcore\b",
            "owner approval": r"\bowner approved\b|\bowner\b.{0,60}\bapprov\w*\b",
            "host-owned provider": r"\bhost owned\b.{0,60}\bprovider\b|\bprovider\b.{0,60}\bhost owned\b",
            "one approved companion/profile": r"\bone approved\b.{0,80}\b(?:companion|profile)\b|\bexactly one\b.{0,80}\b(?:companion|profile)\b",
            "exact credential fields or opaque handles": r"\bexact\b.{0,80}\bcredential field\b|\bopaque handle\b",
            "source selection": r"\bsource selection\b|\bselect\w*\b.{0,50}\bsource\b",
            "refresh after expiry": r"\brefresh\b.{0,80}\bexpir\w*\b|\bexpir\w*\b.{0,80}\brefresh\b",
            "identity changes": r"\bidentity change\b|\bidentity\b.{0,50}\bchange\b",
            "revocation": r"\brevoc\w*\b",
            "no Docker socket": r"\bdocker socket\b",
            "no general container inspection": r"\bcontainer inspection\b",
            "no arbitrary environment/filesystem reads": r"\b(?:environment|filesystem)\b.{0,60}\bread\b|\barbitrary\b.{0,80}\b(?:environment|filesystem)\b",
            "no shell execution": r"\bshell execution\b|\bshell\b",
            "no nodes.call substitute": r"\bnodes call\b",
            "explicit least-privilege denials": r"\b(?:no|not|never|forbid|forbidden|reject)\b.{0,240}\bdocker socket\b|\bdocker socket\b.{0,240}\b(?:forbidden|not allowed|rejected)\b",
            "manual entry is not automatic-refresh parity": r"\bmanual\b.{0,100}\b(?:not|never|no)\b.{0,100}\b(?:automatic refresh|refresh feature|parity)\b|\bmanual\b.{0,100}\b(?:automatic refresh|refresh feature)\b.{0,100}\b(?:not|never|no)\b",
        })

    def test_sensitive_session_subcontract_is_independent(self):
        text = self.section("sensitive session API")
        self.assert_concepts(text, {
            "separate explicit grants": r"\bexplicit grant\b|\bseparate\b.{0,60}\bgrant\b",
            "reviewed schemas": r"\breviewed schema\b|\bbounded schema\b|\bschema\b.{0,60}\breview\w*\b",
            "no inherited core authority": r"\b(?:not|never|no)\b.{0,100}\binherit\w*\b.{0,100}\bcore\b|\bcore\b.{0,100}\b(?:not|never|no)\b.{0,80}\bauthoriz|\bindependent\b.{0,80}\bcore\b",
            "rename separated from mode changes": r"\brename\b.{0,100}\bseparat\w*\b.{0,100}\bmode\b|\bmode\b.{0,100}\bseparat\w*\b.{0,100}\brename\b",
            "ordinary session scopes are separate": r"\bordinary session\b.{0,100}\b(?:read|control|scope)\b|\b(?:read|control)\b.{0,80}\bordinary\b.{0,50}\bscope\b",
            "explicit rename target": r"\brename\b.{0,80}\btarget\b|\btarget\b.{0,80}\brename\b",
            "bounded rename schema": r"\brename\b.{0,80}\bbounded schema\b|\bbounded schema\b.{0,80}\brename\b",
            "ABA-safe revision mutation fences": r"\brevision\b.{0,100}\bcompare and swap\b|\bcompare and swap\b.{0,100}\brevision\b",
            "ABA sequences make tickets stale": r"\ba b a\b",
            "genuine account-owner/user authorization": r"\b(?:account owner|account owner user|owner user)\b.{0,100}\b(?:explicit|genuine)\b.{0,60}\bauthoriz|\b(?:explicit|genuine)\b.{0,80}\b(?:account owner|owner user)\b",
            "operation-specific ticket": r"\boperation specific\b.{0,60}\bticket\b|\bticket\b.{0,60}\boperation specific\b",
            "expiring ticket": r"\bticket\b.{0,80}\bexpir\w*\b|\bexpir\w*\b.{0,80}\bticket\b",
            "caller ticket binding": r"\bcaller\b",
            "exact-session ticket binding": r"\bexact session\b",
            "current mode ticket binding": r"\bcurrent mode\b|\bcurrent\b.{0,40}\brequested mode\b",
            "requested mode ticket binding": r"\brequested mode\b",
            "request-ID ticket binding": r"\brequest id\b",
            "permitted-use ticket binding": r"\bpermitted use\b",
            "stale-ticket refusal": r"\bstale ticket\b.{0,80}\b(?:refus|reject|deny)\w*\b|\b(?:refus|reject|deny)\w*\b.{0,80}\bstale ticket\b",
            "target-substitution refusal": r"\btarget substitution\b.{0,80}\b(?:refus|reject|deny)\w*\b|\b(?:refus|reject|deny)\w*\b.{0,80}\btarget substitution\b",
            "automatic elevation forbidden": r"\bautomatic elevation\b.{0,80}\b(?:refus|reject|deny|forbid|never|not)\w*\b|\b(?:refus|reject|deny|forbid|never|not)\w*\b.{0,80}\bautomatic elevation\b",
            "modes are not enabled by default": r"\b(?:not|never|no)\b.{0,80}\benable\w*\b.{0,60}\bdefault\b|\bdefault\b.{0,80}\bnot\b.{0,60}\bmode\b",
            "no broad existing-scope addition": r"\bbroad\b.{0,60}\bscope\b|\bexisting scope\b",
            "maintainer decision is not owner consent": r"\bmaintainer\b.{0,120}\b(?:not|never|no)\b.{0,80}\b(?:consent|authoriz|permission)\b|\b(?:consent|authoriz|permission)\b.{0,100}\bmaintainer\b.{0,80}\bnot\b",
        })

    def test_isolation_and_redacted_audit(self):
        text = self.section("isolation and audit")
        self.assert_concepts(text, {
            "cross-extension handle rejection": r"\bcross extension\b.{0,100}\bhandle\b.{0,80}\b(?:reject|refus|deny)\w*\b|\b(?:reject|refus|deny)\w*\b.{0,100}\bcross extension\b",
            "cross-account handle rejection": r"\bcross account\b.{0,100}\bhandle\b.{0,80}\b(?:reject|refus|deny)\w*\b|\b(?:reject|refus|deny)\w*\b.{0,100}\bcross account\b",
            "stale-generation rejection": r"\bstale generation\b.{0,80}\b(?:reject|refus|deny)\w*\b|\b(?:reject|refus|deny)\w*\b.{0,80}\bstale generation\b",
            "forged-confirmation rejection": r"\bforg\w*\b.{0,50}\bconfirmation\b.{0,80}\b(?:reject|refus|deny)\w*\b|\b(?:reject|refus|deny)\w*\b.{0,80}\bforg\w*\b.{0,50}\bconfirmation\b",
            "redacted audit categories": r"\bredact\w*\b.{0,80}\b(?:audit|categor)\w*\b|\b(?:audit|categor)\w*\b.{0,80}\bredact\w*\b",
            "caller/grant lifecycle audit": r"\bcaller\b.{0,80}\bgrant lifecycle\b|\bgrant lifecycle\b",
            "budget-usage audit": r"\bbudget usage\b|\bbudget\b.{0,50}\busage\b",
            "outcome audit": r"\boutcome\b",
            "no credential auditing": r"\b(?:not|never|no)\b.{0,120}\bcredential\b|\bcredential\b.{0,120}\b(?:not audited|excluded|redacted)\b",
            "no message-body auditing": r"\bmessage bod\w*\b.{0,80}\b(?:not|never|no|excluded|redacted)\b|\b(?:not|never|no)\b.{0,100}\bmessage bod\w*\b",
            "no media-content auditing": r"\bmedia content\b.{0,80}\b(?:not|never|no|excluded|redacted)\b|\b(?:not|never|no)\b.{0,100}\bmedia content\b",
        })

    def test_traceable_implementation_acceptance_matrix(self):
        text = self.section("implementation acceptance")
        matrices = []
        for table in markdown_tables(text):
            indices = {
                "id": column(table, r"^(?:case |test |acceptance )?id$"),
                "scenario": column(table, r"^(?:scenario|case|behavior|behaviour)$"),
                "expected": column(table, r"expected|assertion|outcome"),
                "reference": column(table, r"contract|requirement|section|source"),
                "evidence": column(table, r"test level|level|layer|evidence|acceptance"),
            }
            if indices["id"] is not None and indices["scenario"] is not None and indices["expected"] is not None:
                self.assertIsNotNone(indices["reference"], f"acceptance table at line {table['line']} lacks contract/requirement traceability")
                self.assertIsNotNone(indices["evidence"], f"acceptance table at line {table['line']} lacks test-level/evidence")
                matrices.append((table, indices))
        self.assertTrue(matrices, "implementation acceptance needs an ID/scenario/expected/reference/evidence matrix")

        rows = []
        seen_ids = set()
        for table, indices in matrices:
            for raw in table["rows"]:
                row_id = raw[indices["id"]].strip().strip("`")
                with self.subTest(matrix_row=row_id or f"line {table['line']}"):
                    self.assertRegex(row_id, r"^[A-Za-z][A-Za-z0-9]*(?:-[A-Za-z0-9]+)+$",
                                     "each acceptance case needs a stable, readable ID")
                    self.assertNotIn(row_id, seen_ids, "acceptance case IDs must be unique")
                    for name in ("scenario", "expected", "reference", "evidence"):
                        value = normalized(raw[indices[name]])
                        self.assertTrue(value and not PLACEHOLDER.fullmatch(value), f"acceptance row {row_id} lacks {name}")
                seen_ids.add(row_id)
                rows.append({"id": row_id, "text": normalized(" ".join(raw))})
        self.assertTrue(rows, "the acceptance matrix must contain executable downstream cases")

        coverage = list(rows)
        for prefix in ("E2E", "STREAM", "DURABLE", "RESTART", "CRED", "SESSION"):
            related = [row for row in rows if row["id"].upper().startswith(prefix + "-")]
            if related:
                coverage.append({"id": prefix, "text": " ".join(row["text"] for row in related)})

        def covered(*patterns):
            return any(all(re.search(pattern, row["text"]) for pattern in patterns) for row in coverage)

        cases = {
            "forged principals": (r"\bforg\w*\b.{0,60}\b(?:principal|caller|identity)\b|\b(?:principal|caller|identity)\b.{0,60}\bforg\w*\b",),
            "undeclared routes": (r"\bundeclared\b.{0,60}\broute\b|\broute\b.{0,60}\bundeclared\b",),
            "path collisions": (r"\bpath\b.{0,60}\bcollision\b|\bcollision\b.{0,60}\bpath\b",),
            "wrong installation": (r"\b(?:wrong|other|mismatched|cross)\b.{0,50}\binstallation\b|\binstallation\b.{0,50}\b(?:wrong|mismatch|cross)\b",),
            "wrong account": (r"\b(?:wrong|other|mismatched|cross)\b.{0,50}\baccount\b|\baccount\b.{0,50}\b(?:wrong|mismatch|cross)\b",),
            "stale/wrong generation": (r"\b(?:wrong|stale|old|mismatched)\b.{0,50}\bgeneration\b|\bgeneration\b.{0,50}\b(?:wrong|stale|mismatch)\b",),
            "cross-extension handles": (r"\bcross extension\b.{0,80}\bhandle\b|\bhandle\b.{0,80}\bcross extension\b",),
            "bad tickets": (r"\b(?:bad|stale|forged|invalid|expired)\b.{0,50}\bticket\b|\bticket\b.{0,50}\b(?:bad|stale|forged|invalid|expired)\b",),
            "session target substitution": (r"\btarget substitution\b|\bsubstitut\w*\b.{0,50}\b(?:session|target)\b",),
            "denied media targets": (r"\b(?:denied|blocked|rejected)\b.{0,60}\bmedia target\b|\bmedia target\b.{0,60}\b(?:denied|blocked|rejected)\b",),
            "redirects": (r"\bredirect\b",),
            "oversized frames": (r"\b(?:oversized|too large|exceed\w*)\b.{0,60}\bframe\b|\bframe\b.{0,60}\b(?:oversized|too large|exceed\w*)\b",),
            "encoded frames": (r"\bencoded\b.{0,60}\bframe\b|\bframe\b.{0,60}\bencoded\b",),
            "late callbacks/results": (r"\blate\b.{0,50}\b(?:callback|result)\b",),
            "forged confirmations": (r"\bforg\w*\b.{0,50}\bconfirmation\b",),
            "credential refresh and revocation acceptance": (r"\bcredential\b|\bcompanion\b", r"\brefresh\b", r"\brevoc\w*\b"),
            "streaming": (r"\bstream\w*\b",),
            "Range and partial statuses": (r"\brange\b", r"\b206\b", r"\b416\b"),
            "HEAD": (r"\bhead\b",),
            "cache validators": (r"\bcache\b.{0,60}\b(?:validator|etag|last modified)\b|\betag\b|\blast modified\b",),
            "streaming/media errors": (r"\b(?:stream\w*|media)\b.{0,100}\berror\b|\berror\b.{0,100}\b(?:stream\w*|media)\b",),
            "backpressure": (r"\bbackpressure\b",),
            "disconnects": (r"\bdisconnect\b",),
            "cancellation": (r"\bcancel\w*\b",),
            "reservation-save failure means no external effect": (r"\breservation\b", r"\b(?:save|persist|write)\w*\b.{0,60}\bfail\w*\b|\bfail\w*\b.{0,60}\b(?:save|persist|write)\w*\b", r"\bno external effect\b"),
            "ambiguous outcome without duplicate/new-ID writes": (r"\b(?:ambiguous|unknown|indeterminate)\b", r"\bduplicate\b", r"\bnew id\b|\breplay\b"),
            "real Postgres restart and durable receipts": (r"\breal postgres\b", r"\brestart\b", r"\breceipt\b|\bdurable\b"),
            "manager restart and durable receipts": (r"\bmanager\b", r"\brestart\b", r"\breceipt\b|\bdurable\b"),
            "installed source isolated-hub synthetic E2E with captured flows": (
                r"\binstalled\b.{0,60}\bsource\b|\bsource profile\b",
                r"\bisolated\b.{0,60}\b(?:development |dev )?hub\b",
                r"\bsynthetic\b",
                r"\bend to end\b|\be2e\b",
                r"\bauthenticat\w*\b",
                r"\bmedia\b",
                r"\bconfirmation\b",
                r"\b(?:captur\w*|inspect\w*)\b",
            ),
        }
        for label, patterns in cases.items():
            with self.subTest(acceptance_coverage=label):
                self.assertTrue(covered(*patterns), f"acceptance matrix has no traceable row/series covering {label}")

    def test_acceptance_matrix_rejects_blank_scenario(self):
        case = SourceApiContractTests(methodName="test_traceable_implementation_acceptance_matrix")
        matrix = """| Case ID | Scenario | Expected assertion | Contract section | Test level/evidence |
| --- | --- | --- | --- | --- |
| NEG-EXAMPLE |  | No dispatch occurs | 2 | Manager admission test |
"""
        with patch.object(case, "section", return_value=matrix):
            with self.assertRaisesRegex(AssertionError, "lacks scenario"):
                case.test_traceable_implementation_acceptance_matrix()

    def test_design_track_phase_boundary(self):
        text = self.section("phase boundary")
        self.assert_concepts(text, {
            "design-only scope": r"\bdesign\b.{0,80}\bonly\b|\bonly\b.{0,80}\bdesign\b|\bdesign track\b",
            "no production routes": r"\bproduction\b.{0,100}\broute\b|\broute\b.{0,100}\bproduction\b",
            "no grant changes": r"\bgrant\b",
            "no SDK capability addition": r"\bsdk\b.{0,80}\bcapabilit|\bcapabilit\w*\b.{0,80}\bsdk\b",
            "no activation": r"\bactivation\b",
            "no migration": r"\bmigration\b",
            "no implementation authorization": r"\b(?:no|not|never|without)\b.{0,100}\bimplement\w*\b|\bimplement\w*\b.{0,100}\b(?:not authorized|not permitted|blocked|after)\b",
            "default-package changes remain gated": r"\bdefault package\b",
            "marketplace/signing/production isolation remains separate": r"\b(?:marketplace|signing|re signing)\b.{0,160}\b1151\b|\b1151\b.{0,160}\b(?:marketplace|signing|re signing)\b",
        })


if __name__ == "__main__":
    unittest.main()
