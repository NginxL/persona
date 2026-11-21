package node

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A plain `go test ./internal/node` runs this package's tests in concurrent processes of its own binary (#5734). They
// would run one after another: almost none can call t.Parallel (most set the environment or swap a package variable),
// and the serial run of about 2,500 top-level tests takes some 19 minutes, past a verifier's ten. So, as CI's node shard
// does (scripts/test/go_partitions.py), the binary lists its tests and runs them in partitions, each a process with an
// exact -run list, split round-robin (the slow ones, listed together by file, spread out). Each partition's output is
// printed whole when it ends, and the run fails when any partition does. A run that picks its tests (-run: the shard's
// partitions, a helper process), lists, benchmarks, fuzzes, profiles, measures coverage or is verbose (-v, -json) runs
// as before, and so does any run with NODE_TEST_PARTITIONS=1.

// partitionEnv marks a partition's process and the listing's: they run as before.
const partitionEnv = "NODE_TEST_PARTITION"

// testName is what -run selects from a -test.list line: tests, examples and fuzz seeds (benchmarks need -bench).
var testName = regexp.MustCompile(`^(?:Test|Example|Fuzz)\w*$`)

// testPartitions is how many processes share the tests: NODE_TEST_PARTITIONS when it is a number, else half the CPUs,
// at least three and at most six (matching the CI shard's three on its four-CPU runner). The tests mostly wait on
// processes and timers: on an 18-CPU Mac six took 265–281 s, four 399–460 s and one 1,100–1,500 s. Two later took
// about 570 s each on the CI lane, beyond its 600-second budget after startup. Fewer than two runs in this process.
func testPartitions() int {
	if n, err := strconv.Atoi(os.Getenv("NODE_TEST_PARTITIONS")); err == nil {
		return n
	}
	return partitionCount(runtime.NumCPU())
}

func partitionCount(cpus int) int {
	return min(max(cpus/2, 3), 6)
}

// TestPartitionDefaultMatchesCI keeps the plain run and scripts/test/go_partitions.py's three node partitions aligned.
func TestPartitionDefaultMatchesCI(t *testing.T) {
	for cpus, want := range map[int]int{1: 3, 2: 3, 4: 3, 6: 3, 12: 6, 64: 6} {
		if got := partitionCount(cpus); got != want {
			t.Errorf("%d CPUs: partitions = %d, want %d", cpus, got, want)
		}
	}
	t.Setenv("NODE_TEST_PARTITIONS", "")
	if got := testPartitions(); got != partitionCount(runtime.NumCPU()) {
		t.Fatalf("default partitions = %d, want %d", got, partitionCount(runtime.NumCPU()))
	}
	for _, count := range []string{"1", "3"} {
		t.Setenv("NODE_TEST_PARTITIONS", count)
		if got := testPartitions(); got != int(count[0]-'0') {
			t.Fatalf("NODE_TEST_PARTITIONS=%s gave %d", count, got)
		}
	}
}

// runPartitioned runs the tests in partitions when this is a plain run of the whole package, and returns the run's exit
// code; ran is false when the tests are to run in this process.
func runPartitioned() (code int, ran bool) {
	if os.Getenv(partitionEnv) != "" {
		return 0, false
	}
	parts := testPartitions()
	if parts < 2 || !plainTestRun() {
		return 0, false
	}
	self, err := os.Executable()
	if err != nil {
		return 0, false
	}
	names, err := listedTests(self)
	if err != nil || len(names) < parts {
		return 0, false
	}
	logFile := testFlag("test.testlogfile")
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		failed   bool
		logs     = make([]string, parts)
		failures = make([]string, parts)
	)
	for i := range parts {
		args, log := partitionArgs(names, parts, i, os.Args[1:], logFile)
		logs[i] = log
		count := (len(names) - i + parts - 1) / parts
		wg.Go(func() {
			cmd := exec.Command(self, args...)
			cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", partitionEnv, i+1))
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			start := time.Now()
			err := cmd.Run()
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprintf(&out, "node test partition %d of %d: %d tests, %.1fs", i+1, parts, count, time.Since(start).Seconds())
			if err != nil {
				failed = true
				fmt.Fprintf(&out, ", %v", err)
				failures[i] = fmt.Sprintf("partition %d of %d (%v): %s", i+1, parts, err, failedTests(out.Bytes()))
			}
			out.WriteString("\n")
			_, _ = os.Stdout.Write(out.Bytes())
		})
	}
	wg.Wait()
	if logFile != "" && joinTestLogs(logFile, logs) != nil {
		failed = true
	}
	if !failed {
		return 0, true
	}
	// Last, where a reader that keeps only the end of the output (a verifier's summary) still sees what failed: a
	// partition's own lines went out when it ended, maybe long before.
	for _, f := range failures {
		if f != "" {
			fmt.Println("node tests failed in " + f)
		}
	}
	return 1, true
}

// failedTest matches a test that failed, at any depth ("--- FAIL: TestX (1.2s)"), and testPanic a partition that
// panicked or ran out of time instead.
var (
	failedTest = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
	testPanic  = regexp.MustCompile(`(?m)^(panic: .*|.*test timed out after .*)$`)
)

// failedTests names what failed in a partition's output: its failed tests (subtests too), else why it stopped.
func failedTests(out []byte) string {
	var names []string
	for _, m := range failedTest.FindAllSubmatch(out, -1) {
		names = append(names, string(m[1]))
	}
	if len(names) == 0 {
		if m := testPanic.Find(out); m != nil {
			return clipText(string(m), 300)
		}
		return "no failed test named in its output"
	}
	return strings.Join(names, ", ")
}

// partitionArgs is the command line of partition i of parts: an exact -run list of every parts-th name from the i-th
// on, and the run's own flags. With a test log (go test's cache keys on what it records), the partition writes its own
// next to the run's, as log.
func partitionArgs(names []string, parts, i int, run []string, logFile string) (args []string, log string) {
	var picked []string
	for j := i; j < len(names); j += parts {
		picked = append(picked, names[j])
	}
	args = []string{"-test.run=^(" + strings.Join(picked, "|") + ")$"}
	for _, a := range run {
		if !strings.HasPrefix(a, "-test.testlogfile=") {
			args = append(args, a)
		}
	}
	if logFile != "" {
		log = fmt.Sprintf("%s.part%d", logFile, i)
		args = append(args, "-test.testlogfile="+log)
	}
	return args, log
}

// plainTestRun: nothing on the command line picks the tests, lists them, measures or reads the run line by line.
func plainTestRun() bool {
	if !flag.Parsed() {
		flag.Parse()
	}
	for _, name := range []string{"test.run", "test.list", "test.bench", "test.fuzz", "test.coverprofile", "test.gocoverdir",
		"test.cpuprofile", "test.memprofile", "test.blockprofile", "test.mutexprofile", "test.trace"} {
		if testFlag(name) != "" {
			return false
		}
	}
	v := testFlag("test.v")
	return v == "" || v == "false"
}

func testFlag(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// listedTests is what this binary would run, in its -test.list order.
func listedTests(self string) ([]string, error) {
	cmd := exec.Command(self, "-test.list=.*")
	cmd.Env = append(os.Environ(), partitionEnv+"=list")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); testName.MatchString(line) {
			names = append(names, line)
		}
	}
	return names, nil
}

// joinTestLogs writes the partitions' test logs as the run's: one header, then every partition's lines.
func joinTestLogs(path string, parts []string) error {
	var all bytes.Buffer
	for i, p := range parts {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_ = os.Remove(p)
		if i > 0 {
			if _, rest, ok := bytes.Cut(b, []byte("\n")); ok { // each starts with the same "# test log" header
				b = rest
			}
		}
		all.Write(b)
	}
	return os.WriteFile(path, all.Bytes(), 0o666)
}

// Every listed test runs in exactly one partition, round-robin, with the run's own flags; a test log goes to the
// partition's own file.
func TestPartitionsRunEveryTestOnce(t *testing.T) {
	names := []string{"TestA", "TestB", "TestC", "TestD", "TestE", "ExampleF", "FuzzG"}
	run := []string{"-test.paniconexit0", "-test.timeout=10m0s", "-test.testlogfile=/x/testlog.txt", "-test.count=1"}
	seen := map[string]int{}
	for i := range 3 {
		args, log := partitionArgs(names, 3, i, run, "/x/testlog.txt")
		pattern, ok := strings.CutPrefix(args[0], "-test.run=")
		if !ok {
			t.Fatalf("partition %d: %q", i, args)
		}
		re := regexp.MustCompile(pattern)
		for _, n := range names {
			if re.MatchString(n) {
				seen[n]++
			}
		}
		if re.MatchString("TestAB") || re.MatchString("XTestA") {
			t.Fatalf("partition %d: %s is not exact", i, pattern)
		}
		want := []string{"-test.paniconexit0", "-test.timeout=10m0s", "-test.count=1", "-test.testlogfile=" + log}
		if !slices.Equal(args[1:], want) || log != fmt.Sprintf("/x/testlog.txt.part%d", i) {
			t.Fatalf("partition %d: %q, log %q", i, args[1:], log)
		}
	}
	for _, n := range names {
		if seen[n] != 1 {
			t.Fatalf("%s ran in %d partitions: %v", n, seen[n], seen)
		}
	}
	if args, log := partitionArgs(names, 3, 0, run[:2], ""); len(args) != 3 || log != "" {
		t.Fatalf("no test log: %q, %q", args, log)
	}
}

// The run's test log is the partitions' together under one header, and theirs go.
func TestPartitionTestLogsJoin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testlog.txt")
	parts := []string{path + ".part0", path + ".part1"}
	writeFile(t, parts[0], "# test log\ngetenv HOME\n")
	writeFile(t, parts[1], "# test log\nopen /etc/hosts\n")
	if err := joinTestLogs(path, parts); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "# test log\ngetenv HOME\nopen /etc/hosts\n" {
		t.Fatalf("joined: %q", b)
	}
	for _, p := range parts {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", p, err)
		}
	}
	if joinTestLogs(path, []string{filepath.Join(dir, "missing")}) == nil {
		t.Fatal("a partition's missing log must fail the run")
	}
}

// What failed in a partition is named at the end of the run: its failed tests and subtests, else its panic or timeout.
func TestPartitionFailuresNamed(t *testing.T) {
	for out, want := range map[string]string{
		"=== RUN   TestA\n--- FAIL: TestA (0.10s)\n    --- FAIL: TestA/sub (0.00s)\n--- PASS: TestB (0.00s)\nFAIL\n": "TestA, TestA/sub",
		"ok\npanic: test timed out after 10m0s\n\trunning tests:\n":                                                  "panic: test timed out after 10m0s",
		"panic: boom\n\ngoroutine 1 [running]:\n":                                                                    "panic: boom",
		"exit status 2\n": "no failed test named in its output",
	} {
		if got := failedTests([]byte(out)); got != want {
			t.Errorf("failedTests(%q) = %q, want %q", out, got, want)
		}
	}
}
