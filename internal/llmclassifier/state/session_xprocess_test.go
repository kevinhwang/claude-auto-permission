//go:build xprocess

// This test spawns multiple subprocesses that all bump the same session counter, verifying flock works across processes
// (not just goroutines within one process).
//
// Build-tagged because it requires re-execing the test binary with a special env var; running the standard `go test`
// invocation would also run the worker code paths and produce noise.
//
// To run: go test -tags xprocess ./internal/llmclassifier/state/...

package state

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
)

const (
	xprocEnvDir       = "CC_PHANDLER_XPROC_DIR"
	xprocEnvSessionID = "CC_PHANDLER_XPROC_SESSION"
	xprocEnvIters     = "CC_PHANDLER_XPROC_ITERS"
)

// TestMain catches re-exec'd subprocesses before they run the test binary's normal main and dispatches them to the
// worker function. In a normal `go test` invocation, the env var is unset and TestMain just calls m.Run(); subprocesses
// we spawn set it.
func TestMain(m *testing.M) {
	if dir := os.Getenv(xprocEnvDir); dir != "" {
		runXProcWorker(dir, os.Getenv(xprocEnvSessionID), atoi(os.Getenv(xprocEnvIters)))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runXProcWorker(dir, sessionID string, iters int) {
	store := New(dir)
	for i := 0; i < iters; i++ {
		// Use a very large limit so the auto-disable path doesn't fire — we're just testing counter accuracy.
		if _, err := store.Block(sessionID, "x", Limits{MaxConsecutive: 1_000_000, MaxTotal: 1_000_000}); err != nil {
			os.Stderr.WriteString("worker Block: " + err.Error() + "\n")
			os.Exit(1)
		}
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func TestStore_CrossProcessConcurrency(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "xproc-sess"
	const procs = 5
	const itersPerProc = 20

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(procs)
	for range procs {
		go func() {
			defer wg.Done()
			cmd := exec.Command(exe)
			cmd.Env = append(os.Environ(),
				xprocEnvDir+"="+dir,
				xprocEnvSessionID+"="+sessionID,
				xprocEnvIters+"="+strconv.Itoa(itersPerProc),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("subprocess failed: %v\n%s", err, out)
			}
		}()
	}
	wg.Wait()

	snap, err := New(dir).Get(sessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := procs * itersPerProc
	if snap.TotalBlocks != want {
		t.Errorf("TotalBlocks = %d, want %d (lost updates indicate flock isn't holding across processes)",
			snap.TotalBlocks, want)
	}
}
