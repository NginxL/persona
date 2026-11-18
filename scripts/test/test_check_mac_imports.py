"""Acceptance tests for scripts/release/check_mac_imports.py (#9948).

An iPhone app running on an Apple silicon Mac must not strongly import a symbol that the Mac lacks, such as
LiveCommunicationKit's ConversationManagerDelegate.conversationManager(_:didActivate:) method descriptor. Weak
imports are allowed because the dynamic loader can leave them unresolved.

The future public API exercised here is ``find_missing_imports(imports_text, linked_dylibs, tbd_texts)``:
- ``imports_text`` has the shape of ``xcrun dyld_info -imports`` output;
- ``linked_dylibs`` maps the names in its ``(from ...)`` fields to their install names;
- ``tbd_texts`` maps MacOSX.sdk-relative .tbd paths to their text.
It returns ``(install_name, symbol)`` pairs, in import order, for strong system imports absent from the selected Mac
TBDs. An empty list is the clean verdict, for which the command prints nothing. The implementation is otherwise free
to choose its parsing and lookup strategy. These synthetic strings need neither Xcode nor a Mach-O file.
"""

from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "release"))
import check_mac_imports as cmi  # noqa: E402

STRONG = "0x00"
WEAK = "0x01"
KIT = "/System/Library/Frameworks/ExampleConversationKit.framework/ExampleConversationKit"
OTHER_KIT = "/System/Library/Frameworks/ExampleMediaKit.framework/ExampleMediaKit"
SUPPORT_KIT = "/System/Library/Frameworks/ExampleConversationSupportKit.framework/ExampleConversationSupportKit"
PHONE_DYLIB = "/usr/lib/libExamplePhone.dylib"


def imports_text(*entries):
    """Small dyld_info -imports fixtures: (flags, symbol, name in the from field) tuples."""
    lines = ["ExampleExecutable [arm64]:"]
    for index, (flags, symbol, source) in enumerate(entries):
        lines.append(f"    0x000000010000{index:04x}  {flags}  {symbol}  (from {source})")
    return "\n".join(lines) + "\n"


def tbd_text(install_name, symbols=(), reexports=(), target="arm64e-maccatalyst", additional_exports=()):
    """A TBD v4 fixture with direct symbols and, optionally, reexported install names."""
    lines = [
        "--- !tapi-tbd",
        "tbd-version: 4",
        f"targets: [ {target} ]",
        f"install-name: '{install_name}'",
        "exports:",
        f"  - targets: [ {target} ]",
        f"    symbols: [ {', '.join(symbols)} ]",
    ]
    for export_target, export_symbols in additional_exports:
        lines += [
            f"  - targets: [ {export_target} ]",
            f"    symbols: [ {', '.join(export_symbols)} ]",
        ]
    if reexports:
        libraries = ", ".join(f"'{name}'" for name in reexports)
        lines += [
            "reexported-libraries:",
            f"  - targets: [ {target} ]",
            f"    libraries: [ {libraries} ]",
        ]
    lines.append("...")
    return "\n".join(lines) + "\n"


def tbd_path(install_name, ios_support=False):
    """The SDK-relative TBD path for a framework or dylib install name."""
    relative = install_name.lstrip("/")
    if relative.endswith(".dylib"):
        relative = relative[:-len(".dylib")] + ".tbd"
    else:
        relative += ".tbd"
    return ("System/iOSSupport/" if ios_support else "") + relative


def findings(entries, linked, tbds):
    return cmi.find_missing_imports(imports_text(*entries), linked, tbds)


class MacImportTests(unittest.TestCase):
    def test_strong_missing_import_is_reported_but_weak_missing_import_is_ignored(self):
        phone_only = "_ExamplePhoneOnlyActivationDescriptor"
        weak_only = "_ExampleOptionalPhoneOnlyDescriptor"
        found = findings([
            (STRONG, phone_only, "ExampleConversationKit"),
            (WEAK, weak_only, "ExampleConversationKit"),
        ], {"ExampleConversationKit": KIT}, {
            tbd_path(KIT): tbd_text(KIT, symbols=["_ExampleUnrelatedExport"]),
        })
        self.assertEqual(found, [(KIT, phone_only)])

    def test_dyld_info_weak_import_attribute_matches_real_tool_output(self):
        strong = "_ExampleStrongMissing"
        imports = (
            "ExampleExecutable [arm64]:\n"
            "    -imports:\n"
            f"      0x011A  {strong}  (from ExampleConversationKit)\n"
            "      0x011B  _ExampleWeakMissing [weak-import]  (from ExampleConversationKit)\n"
        )
        found = cmi.find_missing_imports(imports, {"ExampleConversationKit": KIT}, {
            tbd_path(KIT): tbd_text(KIT),
        })
        self.assertEqual(found, [(KIT, strong)])

    def test_system_dylib_imports_are_checked_for_present_and_absent_symbols(self):
        present = "_ExamplePhoneLibraryAvailable"
        absent = "_ExamplePhoneLibraryMissing"
        found = findings([
            (STRONG, present, "libExamplePhone.dylib"),
            (STRONG, absent, "libExamplePhone.dylib"),
        ], {"libExamplePhone.dylib": PHONE_DYLIB}, {
            tbd_path(PHONE_DYLIB): tbd_text(PHONE_DYLIB, symbols=[present], target="arm64e-macos"),
        })
        self.assertEqual(found, [(PHONE_DYLIB, absent)])

    def test_no_findings_when_every_strong_system_import_is_exported(self):
        exported = "_ExampleConversationAvailable"
        found = findings([
            (STRONG, exported, "ExampleConversationKit"),
            (WEAK, "_ExampleOptionalButAbsent", "ExampleConversationKit"),
            (STRONG, "_ExampleBundledButAbsent", "ExampleBundledKit"),
            (STRONG, "_ExampleCoalescedDefinition", "<weak-def-coalesce>"),
        ], {
            "ExampleConversationKit": KIT,
            "ExampleBundledKit": "@rpath/ExampleBundledKit.framework/ExampleBundledKit",
        }, {
            tbd_path(KIT): tbd_text(KIT, symbols=[exported]),
        })
        self.assertEqual(found, [], "a clean check has no findings and therefore nothing to print")

    def test_missing_symbols_are_attributed_to_each_importing_framework(self):
        shared = "_ExampleSharedEntry"
        absent = "_ExampleAbsentEverywhere"
        found = findings([
            (STRONG, shared, "ExampleConversationKit"),
            (STRONG, shared, "ExampleMediaKit"),
            (STRONG, absent, "ExampleConversationKit"),
            (STRONG, absent, "ExampleMediaKit"),
        ], {
            "ExampleConversationKit": KIT,
            "ExampleMediaKit": OTHER_KIT,
        }, {
            tbd_path(KIT): tbd_text(KIT, symbols=[shared]),
            tbd_path(OTHER_KIT): tbd_text(OTHER_KIT),
        })
        self.assertEqual(found, [
            (OTHER_KIT, shared),
            (KIT, absent),
            (OTHER_KIT, absent),
        ])

    def test_direct_and_reexported_tbd_symbols_count_as_exports(self):
        direct = "_ExampleDirectConversationEntry"
        reexported = "_ExampleReexportedConversationEntry"
        absent = "_ExampleNeverExportedConversationEntry"
        found = findings([
            (STRONG, direct, "ExampleConversationKit"),
            (STRONG, reexported, "ExampleConversationKit"),
            (STRONG, absent, "ExampleConversationKit"),
        ], {"ExampleConversationKit": KIT}, {
            tbd_path(KIT): tbd_text(KIT, symbols=[direct], reexports=[SUPPORT_KIT]),
            tbd_path(SUPPORT_KIT): tbd_text(SUPPORT_KIT, symbols=[reexported]),
        })
        self.assertEqual(found, [(KIT, absent)])

    def test_ios_support_tbd_is_preferred_to_the_macos_copy(self):
        ios_only = "_ExampleAvailableForiPhoneAppsOnMac"
        mac_only = "_ExampleUnavailableForiPhoneAppsOnMac"
        found = findings([
            (STRONG, ios_only, "ExampleConversationKit"),
            (STRONG, mac_only, "ExampleConversationKit"),
        ], {"ExampleConversationKit": KIT}, {
            tbd_path(KIT): tbd_text(KIT, symbols=[mac_only]),
            tbd_path(KIT, ios_support=True): tbd_text(KIT, symbols=[ios_only], target="arm64e-maccatalyst"),
        })
        self.assertEqual(found, [(KIT, mac_only)])

    def test_exports_from_another_cpu_target_do_not_satisfy_apple_silicon(self):
        apple_silicon = "_ExampleAppleSiliconEntry"
        intel_only = "_ExampleIntelOnlyEntry"
        macos_only = "_ExampleMacOSOnlyFrameworkEntry"
        found = findings([
            (STRONG, apple_silicon, "ExampleConversationKit"),
            (STRONG, intel_only, "ExampleConversationKit"),
            (STRONG, macos_only, "ExampleConversationKit"),
        ], {"ExampleConversationKit": KIT}, {
            tbd_path(KIT): tbd_text(
                KIT,
                symbols=[apple_silicon],
                target="arm64e-maccatalyst",
                additional_exports=[
                    ("x86_64-macos", [intel_only]),
                    ("arm64e-macos", [macos_only]),
                ],
            ),
        })
        self.assertEqual(found, [(KIT, intel_only), (KIT, macos_only)])

    def test_ios_support_swift_overlay_follows_its_ios_support_reexports(self):
        overlay = "/usr/lib/swift/libswiftExampleOverlay.dylib"
        overlay_source = "libswiftExampleOverlay.dylib"
        support_framework = "/System/iOSSupport/System/Library/Frameworks/ExampleUIKit.framework/ExampleUIKit"
        support_framework_tbd = "System/iOSSupport/System/Library/Frameworks/ExampleUIKit.framework/ExampleUIKit.tbd"
        exported = "_ExampleAvailableThroughUIKit"
        missing = "_ExampleMissingFromUIKit"
        found = findings([
            (STRONG, exported, overlay_source),
            (STRONG, missing, overlay_source),
        ], {overlay_source: overlay}, {
            tbd_path(overlay, ios_support=True): tbd_text(
                "/System/iOSSupport" + overlay,
                reexports=[support_framework],
                target="arm64e-maccatalyst",
            ),
            support_framework_tbd: tbd_text(
                support_framework,
                symbols=[exported],
                target="arm64e-maccatalyst",
            ),
        })
        self.assertEqual(found, [(overlay, missing)])


if __name__ == "__main__":
    unittest.main()
