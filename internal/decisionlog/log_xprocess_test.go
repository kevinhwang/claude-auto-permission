//go:build xprocess

// This test spawns multiple subprocesses that all append to the same decision log, verifying both:
//
//   - O_APPEND atomicity holds across processes (no torn lines).
//   - flock-guarded rotation works across processes (no rotation storm clobbers any committed lines).
//
// Build-tagged because it requires re-execing the test binary with a special env var. Running the standard `go test`
// invocation would also run the worker code paths and produce noise.
//
// To run: go test -tags xprocess ./internal/decisionlog/...

package decisionlog

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"claude-auto-permission/internal/decider"
)

const (
	xprocEnvPath  = "CC_PHANDLER_XPROC_LOG_PATH"
	xprocEnvIters = "CC_PHANDLER_XPROC_LOG_ITERS"
	xprocEnvCap   = "CC_PHANDLER_XPROC_LOG_CAP"
	xprocEnvLabel = "CC_PHANDLER_XPROC_LOG_LABEL"
)

// TestMain catches re-exec'd subprocesses before they run the test binary's normal main and dispatches them to the
// worker function. In a normal `go test` invocation, the env var is unset and TestMain just calls m.Run(); subprocesses
// we spawn set it.
func TestMain(m *testing.M) {
	if path := os.Getenv(xprocEnvPath); path != "" {
		runXProcWorker(
			path,
			os.Getenv(xprocEnvLabel),
			atoi(os.Getenv(xprocEnvIters)),
			int64(atoi(os.Getenv(xprocEnvCap))),
		)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runXProcWorker writes `iters` entries to `path`, each tagged with `label` so the parent test can verify per-worker
// line counts. `cap` is the rotation size cap; pass 0 to disable rotation.
func runXProcWorker(path, label string, iters int, cap int64) {
	w := New(path, cap)
	for i := 0; i < iters; i++ {
		err := w.Append(Entry{
			SessionId: label,
			Tool:      "Bash",
			Decision:  decider.DecisionAllow,
			Reason:    "xproc-iter-" + strconv.Itoa(i),
			Deciders: map[string]DeciderEntry{
				"static_bash_rules": {Decision: decider.DecisionAllow, Reason: "ok"},
			},
		})
		if err != nil {
			os.Stderr.WriteString("worker Append: " + err.Error() + "\n")
			os.Exit(1)
		}
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// TestWriter_CrossProcessConcurrency_NoTearing pins the O_APPEND atomicity guarantee across processes: with rotation
// disabled, every appended entry must land on disk as a complete, valid JSON line. Five processes each write 200
// entries — if any line is torn, json.Unmarshal will fail or the line count will be off.
func TestWriter_CrossProcessConcurrency_NoTearing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.log.jsonl")
	const procs = 5
	const itersPerProc = 200

	runWorkers(t, path, procs, itersPerProc, 0)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	got := 0
	perLabel := map[string]int{}
	for scanner.Scan() {
		line := scanner.Bytes()
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Errorf("malformed line %d: %v\nraw: %s", got, err, line)
			continue
		}
		got++
		perLabel[e.SessionId]++
	}
	want := procs * itersPerProc
	if got != want {
		t.Errorf("total lines = %d, want %d (lost lines indicate O_APPEND atomicity broke across processes)", got, want)
	}
	for i := range procs {
		label := "worker-" + strconv.Itoa(i)
		if perLabel[label] != itersPerProc {
			t.Errorf("%s wrote %d lines, want %d", label, perLabel[label], itersPerProc)
		}
	}
}

// TestWriter_CrossProcessConcurrency_RotationSurvives stresses the flock-guarded rotation path under a multi-process
// load: a small size cap forces near-constant rotation while five processes hammer the active file. Invariants:
//
//   - Every line that lands on disk (active file or .1) is valid JSON.
//   - At least one of the files exists at the end.
//
// We do NOT assert total line preservation here: the rotation retention policy is single-backup (the previous .1 is
// unlinked when a new rotation fires), so lines older than two rotations are intentionally discarded. The test would be
// wrong to demand preservation of everything.
func TestWriter_CrossProcessConcurrency_RotationSurvives(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.log.jsonl")
	const procs = 5
	const itersPerProc = 200
	const sizeCap = 1024 // tiny: each entry trips rotation

	runWorkers(t, path, procs, itersPerProc, sizeCap)

	atLeastOne := false
	for _, p := range []string{path, path + ".1"} {
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		atLeastOne = true
		for line := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			var e Entry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Errorf("malformed line in %s: %v\nraw: %s", p, err, line)
			}
		}
	}
	if !atLeastOne {
		t.Error("both active and rotated files missing after rotation storm")
	}
}

// runWorkers spawns `procs` re-exec'd test-binary subprocesses, each writing `iters` entries to `path`. Blocks until
// they all finish. `cap` is forwarded to the worker as the rotation size cap; 0 to disable.
func runWorkers(t *testing.T, path string, procs, iters int, cap int64) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(procs)
	for i := range procs {
		label := "worker-" + strconv.Itoa(i)
		go func() {
			defer wg.Done()
			cmd := exec.Command(exe)
			cmd.Env = append(os.Environ(),
				xprocEnvPath+"="+path,
				xprocEnvLabel+"="+label,
				xprocEnvIters+"="+strconv.Itoa(iters),
				xprocEnvCap+"="+strconv.FormatInt(cap, 10),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("subprocess %s failed: %v\n%s", label, err, out)
			}
		}()
	}
	wg.Wait()
}
