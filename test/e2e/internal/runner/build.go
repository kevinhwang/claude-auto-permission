package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// BuildBinariesDir compiles the hook binary and the fakeclaude shim into the given directory. Panics on failure —
// intended for use in TestMain where *testing.T is not available.
func BuildBinariesDir(dir string) Binaries {
	root := repoRoot()
	hook := filepath.Join(dir, "claude-auto-permission")
	fakeClaude := filepath.Join(dir, "fakeclaude")

	for _, build := range []struct {
		output string
		pkg    string
	}{
		{hook, "./cmd/claude-auto-permission"},
		{fakeClaude, "./test/e2e/internal/fakeclaude"},
	} {
		cmd := exec.Command("go", "build", "-o", build.output, build.pkg)
		cmd.Dir = root
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			panic(fmt.Sprintf("go build %s: %v", build.pkg, err))
		}
	}

	return Binaries{Hook: hook, FakeClaude: fakeClaude}
}

func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(fmt.Errorf("getwd: %w", err))
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}
