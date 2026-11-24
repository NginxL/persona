#!/usr/bin/env python3
"""Run a `go test` command with the partitioned packages split (scripts/test/test-server.sh on the hub machine,
scripts/test/check-fast.sh's Go checks).

    python3 go_test_partitioned.py go test -count=1 -p 1 [flags] [packages]

internal/node's and internal/assistant's serial runs outgrew Go's ten-minute -timeout (#5328, #5594, #7644). When the
packages are the whole module (`./...`), name one of go_partitions.PACKAGES, or include a `./<dir>/...` that covers one
the runner splits (#12765: check-fast's `./internal/assistant/...`), and no -run narrows the tests, the other packages
run in one `go test` as before and each partitioned package's tests in the partitions go_partitions.py makes for CI's
shards and the release build, all at once. A `./<dir>/...` is expanded with `go list`; a package found there that
splits itself (go_partitions.SPLITS_ITSELF, internal/node) stays with the other packages, where its own TestMain splits
it. Every command keeps the caller's flags (a deploy's -skip, -v, -timeout). Anything else runs exactly as given: this
process becomes that command.

Only this process writes the output: the other packages' lines as they come, each partition's whole when it ends, so
lines never mix and `FAIL <package>` / `--- FAIL` read as in a serial run. The exit status is non-zero when any fails.
"""
import os
import posixpath
import subprocess
import sys
import tempfile
import threading

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # go_partitions sits beside it; -I leaves it off the path
import go_partitions  # noqa: E402

WHOLE_MODULE = "./..."


def is_package(token):
    """A package argument as this repository writes them, used for concise command descriptions."""
    return token.startswith(("./", "../")) or token == "." or token.startswith("github.com/")


# cmd/go/internal/test/testflag.go registers these test flags and their test. aliases. Build flags come from
# cmd/go/internal/work.AddBuildFlags. Only their arity is needed here: bool flags never consume the next argv token,
# while every other known flag does unless its value follows '='. Unknown flags run the original command unchanged.
TEST_VALUE_FLAGS = {
    "bench", "benchtime", "blockprofile", "blockprofilerate", "count", "coverprofile", "cpu", "cpuprofile",
    "fuzz", "fuzzminimizetime", "fuzztime", "list", "memprofile", "memprofilerate", "mutexprofile",
    "mutexprofilefraction", "outputdir", "parallel", "run", "shuffle", "skip", "timeout", "trace",
}
TEST_BOOL_FLAGS = {"artifacts", "benchmem", "failfast", "fullpath", "short", "v"}
VALUE_FLAGS = TEST_VALUE_FLAGS | {
    "asmflags", "buildmode", "compiler", "covermode", "coverpkg", "exec", "gccgoflags", "gcflags",
    "installsuffix", "ldflags", "mod", "modfile", "o", "overlay", "p", "pgo", "pkgdir", "tags", "toolexec", "vet",
    "debug-actiongraph", "debug-runtime-trace", "debug-trace",
}
BOOL_FLAGS = TEST_BOOL_FLAGS | {
    "a", "asan", "buildvcs", "c", "cover", "json", "linkshared", "modcacherw", "msan", "n", "race", "trimpath",
    "work", "x",
}
VALUE_FLAGS |= {"test." + name for name in TEST_VALUE_FLAGS}
BOOL_FLAGS |= {"test." + name for name in TEST_BOOL_FLAGS}
COVERPROFILE_FLAGS = {"coverprofile", "test.coverprofile"}
RUN_FLAGS = {"run", "test.run"}


class ParsedTestArgs:
    def __init__(self):
        self.flags = []
        self.packages = []
        self.trailing = []
        self.passthrough = False
        self.coverprofile = ""
        self.run = ""
        self.valid = True


def split_goflags(value):
    """cmd/internal/quoted.Split: quotes are special only at the start of a field and have no escape syntax."""
    fields = []
    i = 0
    while i < len(value):
        while i < len(value) and value[i] in " \t\n\r":
            i += 1
        if i == len(value):
            break
        if value[i] in "'\"":
            quote = value[i]
            i += 1
            end = value.find(quote, i)
            if end < 0:
                raise ValueError("unterminated %s string" % quote)
            fields.append(value[i:end])
            i = end + 1
        else:
            end = i
            while end < len(value) and value[end] not in " \t\n\r":
                end += 1
            fields.append(value[i:end])
            i = end
    return fields


def flag_token(token):
    """cmd/go/internal/cmdflag.ParseOne's syntax: (name, value, has_value), or None for a non-flag."""
    arg = token[1:] if token.startswith("--") else token
    if len(arg) < 2 or arg[0] != "-" or arg[1] in "-=":
        return None
    name, equals, value = arg[1:].partition("=")
    return name, value, bool(equals)


def apply_flag(parsed, name, value):
    if name in COVERPROFILE_FLAGS:
        parsed.coverprofile = value
    if name in RUN_FLAGS:
        parsed.run = value


def parse_test_args(args, goflags=""):
    """Parse like go test, retaining normalized command pieces and the effective profile/run values.

    The terminator and -args retain their following test-binary arguments. Unknown or malformed flags are marked
    invalid so the caller can conservatively run argv unchanged instead of guessing which token is a flag value.
    """
    parsed = ParsedTestArgs()
    try:
        for token in split_goflags(goflags):
            flag = flag_token(token)
            if flag is None:
                raise ValueError("non-flag %r in GOFLAGS" % token)
            name, value, has_value = flag
            if name not in VALUE_FLAGS | BOOL_FLAGS or name in VALUE_FLAGS and not has_value:
                raise ValueError("unknown flag or missing GOFLAGS value: %s" % token)
            apply_flag(parsed, name, value if has_value else "true")
    except ValueError:
        parsed.valid = False
        return parsed

    i = 0
    while i < len(args):
        token = args[i]
        if token == "--":
            parsed.trailing = list(args[i:])
            break
        if token in ("-args", "--args"):
            # cmd/go removes this marker, so the test binary parses later -test.* flags. Replicating those flags in
            # partitions would repeat selections and make every partition write the same profile.
            parsed.passthrough = True
            parsed.trailing = list(args[i:])
            break
        flag = flag_token(token)
        if flag is None:
            parsed.packages.append(token)
            i += 1
            continue
        name, value, has_value = flag
        if name not in VALUE_FLAGS | BOOL_FLAGS:
            parsed.valid = False
            return parsed
        parsed.flags.append(token)
        if name in VALUE_FLAGS and not has_value:
            if i + 1 == len(args):
                parsed.valid = False
                return parsed
            i += 1
            value = args[i]
            parsed.flags.append(value)
        elif not has_value:
            value = "true"
        apply_flag(parsed, name, value)
        i += 1
    return parsed


def split(args):
    """The go test flags with their values (in order) and package arguments, parsed with Go's flag semantics."""
    parsed = parse_test_args(args)
    return parsed.flags + parsed.trailing, parsed.packages


def has_coverage_profile(args, goflags=""):
    """Conservatively reject commands whose coverage or test-binary flags make partitioning unsafe."""
    parsed = parse_test_args(args, goflags)
    return not parsed.valid or parsed.passthrough or bool(parsed.coverprofile)


def subtree(token):
    """The directory a `./<dir>/...` package argument names, normalized ("internal" for ./internal/...), or None for
    any other argument. ./... itself is the whole module, planned on its own."""
    if token == WHOLE_MODULE or not token.startswith("./") or not token.endswith("/..."):
        return None
    directory = posixpath.normpath(token[:-len("/...")])
    return None if "..." in directory or directory.startswith("../") or directory == ".." else directory


def covers(token):
    """Whether a `./<dir>/...` argument covers a partitioned package that does not split itself."""
    directory = subtree(token)
    if directory is None:
        return False
    for name in go_partitions.PACKAGES:
        package = posixpath.normpath(name)
        if name not in go_partitions.SPLITS_ITSELF and (directory == "." or package == directory or
                                                         package.startswith(directory + "/")):
            return True
    return False


def plan(argv, list_packages, list_patterns, goflags=""):
    """The commands to run for argv: [argv] unchanged, or the other packages' command and one per partition.
    list_packages(patterns) is what `go list <patterns>` prints (the module's packages for ./...), list_patterns(package)
    a partitioned package's -run patterns. goflags is injected by main so pure planner tests never depend on the
    process environment."""
    if argv[:2] != ["go", "test"]:
        return [argv]
    parsed = parse_test_args(argv[2:], goflags)
    if parsed.passthrough:
        return [argv]
    packages = parsed.packages
    whole = WHOLE_MODULE in packages
    subtrees = [p for p in packages if covers(p)]
    if (not parsed.valid or parsed.coverprofile or parsed.run or
            not (whole or subtrees or any(map(go_partitions.is_partitioned, packages)))):
        return [argv]
    named = [p for p in packages if p != WHOLE_MODULE and p not in subtrees]
    listed, own = [], []  # own: the packages a subtree has that split themselves, left to their TestMain
    if whole:
        listed = list_packages([WHOLE_MODULE])  # the subtrees are in it
    elif subtrees:
        for package in list_packages(subtrees):
            (own if go_partitions.partitioned(package) in go_partitions.SPLITS_ITSELF else listed).append(package)
    rest, parts = go_partitions.split(listed + named)
    if not parts:  # go list found no partitioned package under the subtrees: nothing to split
        return [argv]
    rest += [package for package in own if go_partitions.partitioned(package) not in parts]
    commands = [["go", "test", *parsed.flags, *rest, *parsed.trailing]] if rest else []
    return commands + [["go", "test", *parsed.flags, "-run", pattern, package, *parsed.trailing]
                       for package in parts for pattern in list_patterns(package)]


def describe(command):
    """A short line for a command: a partition's -run list is thousands of names."""
    if "-run" in command and command[-1] in go_partitions.PACKAGES:
        pattern = command[command.index("-run") + 1]
        return "%s partition (%d tests)" % (command[-1], pattern.count("|") + 1)
    return "%d package(s)" % sum(map(is_package, command))


def run(commands):
    """Run the commands at once; the first streams, the others print whole when they end. The worst exit status."""
    lock = threading.Lock()

    def emit(text):
        with lock:
            sys.stdout.write(text)
            sys.stdout.flush()

    for command in commands:
        emit("== go test: %s\n" % describe(command))
    first = subprocess.Popen(commands[0], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, errors="replace")
    rest = []
    for command in commands[1:]:
        out = tempfile.TemporaryFile(mode="w+", errors="replace")
        rest.append((command, subprocess.Popen(command, stdout=out, stderr=subprocess.STDOUT, text=True), out))

    def finish(command, process, out):
        status = process.wait()
        out.seek(0)
        emit("== go test: %s, exit %d\n%s" % (describe(command), status, out.read()))
        out.close()

    threads = [threading.Thread(target=finish, args=item) for item in rest]
    for thread in threads:
        thread.start()
    try:
        with first.stdout:
            for line in first.stdout:
                emit(line)
        statuses = [first.wait()]
        for thread in threads:
            thread.join()
    except KeyboardInterrupt:
        for process in [first] + [process for _, process, _ in rest]:
            process.terminate()
        raise
    statuses += [process.returncode for _, process, _ in rest]
    return max(statuses, key=lambda status: status != 0)


def go_list(patterns):
    return subprocess.check_output(["go", "list", *patterns], text=True, timeout=600).split()


def main(argv):
    if not argv:
        print("usage: go_test_partitioned.py go test [flags] [packages]", file=sys.stderr)
        return 2
    try:
        goflags = os.environ.get("GOFLAGS", "")
        # An explicit empty command-line profile can cancel GOFLAGS. Discovery must not recreate the canceled file:
        # go_partitions.list_patterns runs its own go test without the caller's command-line flags.
        discovery_env = None
        if parse_test_args([], goflags).coverprofile:
            discovery_env = os.environ.copy()
            discovery_env["GOFLAGS"] = (goflags + " -coverprofile=").strip()
        commands = plan(argv, go_list,
                        lambda package: go_partitions.list_patterns(os.getcwd(), package, env=discovery_env),
                        goflags=goflags)
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired, ValueError) as err:
        # A package does not build or list (its error is above): run the command as given, which reports it.
        print("go_test_partitioned: could not split %s (%s); running it whole" % (", ".join(go_partitions.PACKAGES),
                                                                                  err), flush=True)
        commands = [argv]
    if len(commands) == 1:
        # Become the command, as if it had been run without this runner: callers see its process, its signals and its
        # exit status (check-fast's timeout and heavy slot wrap go itself).
        sys.stdout.flush()
        os.execvp(commands[0][0], commands[0])
    return run(commands)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
