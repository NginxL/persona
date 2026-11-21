#!/usr/bin/env python3
"""Reject empty, failed or silently skipped Apple unit-test result bundles."""
import argparse
from pathlib import Path
import re
import sys

# Reuse the existing strict xcresulttool test-tree reader. Only the trusted
# repository directory is added, so this helper also works under python -I.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "app"))
from perf_report import query, statuses

# #10261: these two XCTest cases exercise ChatStore's native deinit. A crash
# in another method of the same class, or an ordinary assertion here, is red.
KNOWN_RESTARTS = {
    "PersonaCoreContinuityTests/testEveryContinuityFixtureAgainstNativeChatStore",
    "PersonaCoreContinuityTests/testNativeAndCoreShadowReplayMatchesFoldAndReloadRules",
}
XCTEST_CASE = r"-\[(?:PersonaTests\.)?(\w+) (\w+)\]"


def verify_restarts(log):
    """Xcode may exit zero and omit the crashed case from its xcresult totals."""
    active = set()
    known = set()
    invalid_free = set()
    known_aborts = set()
    unexpected = []
    saw_test_output = False
    for line in log:
        if "Test Suite '" in line or "Test run started" in line:
            saw_test_output = True
            # XCTest and Swift Testing launch distinct suites. A case from a
            # finished suite cannot identify a later unattributed crash.
            active.clear()
            invalid_free.clear()
        event = re.search(r"Test Case '" + XCTEST_CASE + r"' (started|passed|failed|skipped)", line)
        if event:
            case = "/".join(event.groups()[:2])
            if event[3] == "started":
                active.add(case)
            else:
                active.discard(case)
        if "malloc:" in line and "pointer being freed was not allocated" in line and len(active) == 1:
            invalid_free.update(active)
        if "Restarting after unexpected exit" not in line:
            continue
        marker = line.partition("Restarting after unexpected exit")[2].partition(";")[0].strip()
        _, named, location = marker.partition(" in ")
        if named:
            match = re.fullmatch(XCTEST_CASE, location) or re.fullmatch(
                r"(?:PersonaTests[./])?(\w+)[./](\w+)(?:\(\))?", location)
            case = "/".join(match.groups()) if match else location
        else:
            case = next(iter(active)) if len(active) == 1 else "<unattributed>"
        active.clear()
        if case in KNOWN_RESTARTS and case not in known:
            print(f"apple-ci: known test-host restart: {case}")
            known.add(case)
            if case in invalid_free:
                known_aborts.add(case + "()")
        else:
            unexpected.append(case)
        invalid_free.clear()
    if unexpected:
        raise ValueError("unexpected test-host restarts: " + ", ".join(unexpected))
    if not saw_test_output:
        raise ValueError("missing XCTest or Swift Testing output in the test-host log")
    print(f"apple-ci: {len(known)} known test-host restarts, zero unexpected")
    return known_aborts


def verify_destination(bundle, device_id, runner=None, *, known_aborts=(), xcode_status=0):
    summary, state = query(bundle, "summary", runner)
    if state["status"] != "ok" or not isinstance(summary, dict):
        raise ValueError(state.get("error", "unreadable xcresult summary"))
    groups = summary.get("devicesAndConfigurations")
    if not isinstance(groups, list) or not groups:
        raise ValueError("missing xcresult test destination")
    for group in groups:
        device = group.get("device") if isinstance(group, dict) else None
        if not isinstance(device, dict) or (device.get("platform"), device.get("osVersion"), device.get("deviceId")) != (
                "iOS Simulator", "18.6", device_id):
            raise ValueError(f"result did not run on the requested iOS 18.6 simulator {device_id}")
    device = groups[0]["device"]
    print(f"apple-ci: xcresult destination: {device.get('modelName')} iOS 18.6 ({device.get('osBuildNumber')})")
    failures = summary.get("testFailures")
    if summary.get("result") == "Passed" and summary.get("failedTests") == 0 and failures == []:
        if summary.get("skippedTests") == 0 and xcode_status == 0:
            return set()
    # Newer Xcode also records the same host abort as a failed test (exit 65).
    # Only a matching allocator diagnostic, restart and structured failure can
    # account for it. An assertion or a second failure in the same test is red.
    if isinstance(failures, list) and failures and summary.get("result") == "Failed" and xcode_status in (0, 65):
        accounted = {failure["testIdentifierString"] for failure in failures
                     if isinstance(failure, dict) and failure.get("targetName") == "PersonaTests"
                     and failure.get("failureText") == "Test crashed with signal abrt."
                     and failure.get("testIdentifierString") in known_aborts}
        if len(accounted) == len(failures) == summary.get("failedTests") and summary.get("skippedTests") == 0:
            print(f"apple-ci: xcresult records {len(accounted)} known test-host ABRTs (xcodebuild exit {xcode_status})")
            return accounted
    raise ValueError(f"xcresult summary: {summary.get('result')}, {summary.get('failedTests')} failed, "
                     f"{summary.get('skippedTests')} skipped, xcodebuild exit {xcode_status}")


def verify(bundle, required_suites, runner=None, *, known_aborts=()):
    tests, state = statuses(bundle, runner)
    if state["status"] != "ok":
        raise ValueError(state.get("error", "could not read test results"))
    if any(tests.get(name) != "Failed" for name in known_aborts):
        raise ValueError("known crash in the xcresult summary did not match its test case")
    passed = {name for name, status in tests.items() if status == "Passed"}
    bad = [f"{name}: {status}" for name, status in tests.items() if name not in passed and name not in known_aborts]
    if bad:
        raise ValueError("non-passing cases (skips are not passes):\n" + "\n".join(bad))
    for suite in required_suites:
        if not any(suite in name.split("/") for name in passed):
            raise ValueError(f"required suite did not execute: {suite}")
    return len(passed)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path)
    parser.add_argument("required_suites", nargs="*")
    parser.add_argument("--ios18", nargs=2, metavar=("DEVICE_ID", "XCODE_LOG"),
                        help="check the floor runtime and audit its test-host restarts")
    parser.add_argument("--xcode-status", type=int, default=0, help="original iOS 18.6 xcodebuild exit status")
    args = parser.parse_args()
    errors = []
    known_aborts = set()
    accounted = set()
    if args.ios18:
        device, log = args.ios18
        try:
            with Path(log).open(errors="replace") as stream:
                known_aborts = verify_restarts(stream)
        except (ValueError, OSError) as error:
            errors.append(str(error))
        try:
            accounted = verify_destination(args.bundle, device, known_aborts=known_aborts,
                                           xcode_status=args.xcode_status)
        except (ValueError, OSError) as error:
            errors.append(str(error))
    try:
        count = verify(args.bundle, args.required_suites, known_aborts=accounted)
    except (ValueError, OSError) as error:
        errors.append(str(error))
    if errors:
        print(f"apple-ci: FAIL {args.bundle.name}: " + "\n".join(errors), file=sys.stderr)
        return 1
    if accounted:
        print(f"apple-ci: PASS {args.bundle.name}: {count} passed, {len(accounted)} known ABRTs, zero other failures/skips")
    else:
        print(f"apple-ci: PASS {args.bundle.name}: {count} test cases, zero failed/skipped")
    return 0


if __name__ == "__main__":
    sys.exit(main())
