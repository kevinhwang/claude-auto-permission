package automodepolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// installStub writes a static `#!/bin/sh` script under `tempDir/bin/claude` that emits a known canned response from a
// sibling file. The stub dispatches on `auto-mode config` only — that's the single subcommand the loader ever invokes.
func installStub(t *testing.T, configBody string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub-binary tests rely on POSIX /bin/sh")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// %q on the data file is safe — it's a test-managed path under t.TempDir().
	script := fmt.Sprintf(
		"#!/bin/sh\n"+
			"case \"$1\" in\n"+
			"  auto-mode)\n"+
			"    case \"$2\" in\n"+
			"      config) cat %q ;;\n"+
			"    esac ;;\n"+
			"esac\n"+
			"exit 0\n",
		configPath,
	)
	stubPath := filepath.Join(dir, "claude")
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return stubPath
}

// installCwdReportingStub writes a stub that echoes its $PWD wrapped in a Policy-shaped JSON object. Used to assert
// cmd.Dir plumbing.
func installCwdReportingStub(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub-binary tests rely on POSIX /bin/sh")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  auto-mode)\n" +
		"    case \"$2\" in\n" +
		"      config) printf '{\"allow\":[\"cwd=%s\"]}\\n' \"$PWD\" ;;\n" +
		"    esac ;;\n" +
		"esac\n" +
		"exit 0\n"
	stubPath := filepath.Join(dir, "claude")
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return stubPath
}

const sampleConfig = `{
	"allow": ["Allow A", "Allow B"],
	"soft_deny": ["Soft 1"],
	"hard_deny": ["Hard 1", "Hard 2"],
	"environment": ["**Trusted repo**: github.com/me/x"]
}`

func TestLoad_FetchesAndParses(t *testing.T) {
	binary := installStub(t, sampleConfig)
	l := &Loader{
		BinaryPath: binary,
		CacheDir:   t.TempDir(),
	}
	d := l.LoadOrDefaults(context.Background())
	if len(d.Allow) != 2 || d.Allow[0] != "Allow A" {
		t.Errorf("Allow = %v", d.Allow)
	}
	if len(d.SoftDeny) != 1 || d.SoftDeny[0] != "Soft 1" {
		t.Errorf("SoftDeny = %v", d.SoftDeny)
	}
	if len(d.HardDeny) != 2 {
		t.Errorf("HardDeny = %v", d.HardDeny)
	}
}

func TestLoad_CachesResult(t *testing.T) {
	cacheDir := t.TempDir()
	binary := installStub(t, sampleConfig)

	l := &Loader{BinaryPath: binary, CacheDir: cacheDir, Ttl: time.Hour}
	d1 := l.LoadOrDefaults(context.Background())

	// Mutate the underlying response — config.json is a sibling of the stub binary. Cache hit means d2 still matches d1.
	configPath := filepath.Join(filepath.Dir(binary), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"allow":["DIFFERENT"]}`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	d2 := l.LoadOrDefaults(context.Background())
	if d1.Allow[0] != d2.Allow[0] {
		t.Errorf("cache miss on second call: d1.Allow=%v d2.Allow=%v", d1.Allow, d2.Allow)
	}
}

func TestLoad_BinaryMtimeInvalidatesCache(t *testing.T) {
	cacheDir := t.TempDir()
	binary := installStub(t, sampleConfig)

	l := &Loader{BinaryPath: binary, CacheDir: cacheDir, Ttl: time.Hour}
	_ = l.LoadOrDefaults(context.Background())

	// Bump the binary mtime forward a couple of seconds to make sure the nanosecond-resolution mtime is different on every
	// FS.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(binary, future, future); err != nil {
		t.Fatalf("chtimes binary: %v", err)
	}
	configPath := filepath.Join(filepath.Dir(binary), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"allow":["NEW BINARY"]}`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	d := l.LoadOrDefaults(context.Background())
	if len(d.Allow) != 1 || d.Allow[0] != "NEW BINARY" {
		t.Errorf("expected fresh fetch after binary mtime change, got %v", d.Allow)
	}
}

func TestLoad_BinarySizeInvalidatesCache(t *testing.T) {
	cacheDir := t.TempDir()
	binary := installStub(t, sampleConfig)

	l := &Loader{BinaryPath: binary, CacheDir: cacheDir, Ttl: time.Hour}
	_ = l.LoadOrDefaults(context.Background())

	// Capture original mtime, rewrite the binary with different size, then restore the mtime so only size changes.
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("stat binary: %v", err)
	}
	origMtime := info.ModTime()

	bigger, err := os.ReadFile(binary)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	bigger = append(bigger, []byte("\n# trailing comment\n")...)
	if err := os.WriteFile(binary, bigger, 0o755); err != nil {
		t.Fatalf("rewrite binary: %v", err)
	}
	if err := os.Chtimes(binary, origMtime, origMtime); err != nil {
		t.Fatalf("restore mtime: %v", err)
	}

	configPath := filepath.Join(filepath.Dir(binary), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"allow":["BIGGER BINARY"]}`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	d := l.LoadOrDefaults(context.Background())
	if len(d.Allow) != 1 || d.Allow[0] != "BIGGER BINARY" {
		t.Errorf("expected fresh fetch after binary size change, got %v", d.Allow)
	}
}

func TestLoad_SymlinkResolutionStable(t *testing.T) {
	cacheDir := t.TempDir()
	target := installStub(t, sampleConfig)

	// Two distinct symlinks pointing at the same target.
	linkDir := t.TempDir()
	link1 := filepath.Join(linkDir, "claude-link-1")
	link2 := filepath.Join(linkDir, "claude-link-2")
	if err := os.Symlink(target, link1); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(target, link2); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	l1 := &Loader{BinaryPath: link1, CacheDir: cacheDir, Ttl: time.Hour}
	_ = l1.LoadOrDefaults(context.Background())
	// Mutate the response — if both symlinks resolve to the same realpath the second Load should hit cache and ignore the
	// mutation.
	configPath := filepath.Join(filepath.Dir(target), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"allow":["CACHE BUSTED"]}`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	l2 := &Loader{BinaryPath: link2, CacheDir: cacheDir, Ttl: time.Hour}
	d := l2.LoadOrDefaults(context.Background())
	if d.Allow[0] == "CACHE BUSTED" {
		t.Errorf("symlinks resolved to distinct cache keys: got fresh fetch %v", d.Allow)
	}
}

func TestLoad_DistinctCwdsWithMissingSettings(t *testing.T) {
	cacheDir := t.TempDir()
	binary := installStub(t, sampleConfig)

	cwdA := filepath.Join(t.TempDir(), "A")
	cwdB := filepath.Join(t.TempDir(), "B")
	for _, p := range []string{cwdA, cwdB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	pathsA := []string{filepath.Join(cwdA, ".claude", "settings.local.json")}
	pathsB := []string{filepath.Join(cwdB, ".claude", "settings.local.json")}
	// Neither file exists — the path string itself is what distinguishes the keys.

	lA := &Loader{BinaryPath: binary, CacheDir: cacheDir, SettingsPaths: pathsA, Cwd: cwdA, Ttl: time.Hour}
	lB := &Loader{BinaryPath: binary, CacheDir: cacheDir, SettingsPaths: pathsB, Cwd: cwdB, Ttl: time.Hour}

	pathA := mustCachePath(t, lA)
	pathB := mustCachePath(t, lB)
	if pathA == pathB {
		t.Errorf("expected distinct cache paths for distinct cwds, got %s", pathA)
	}
}

// mustCachePath resolves the loader's binary, builds the cache key, and returns the cachePath — exposes the loader's
// internal key derivation for tests that want to assert on it.
func mustCachePath(t *testing.T, l *Loader) string {
	t.Helper()
	_, real, info, err := l.resolveBinary()
	if err != nil {
		t.Fatalf("resolveBinary: %v", err)
	}
	key := buildCacheKey(real, info, l.SettingsPaths)
	path, err := l.cachePath(key)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	return path
}

func TestLoad_PassesCwdToClaude(t *testing.T) {
	binary := installCwdReportingStub(t)
	cwd := t.TempDir()

	l := &Loader{
		BinaryPath: binary,
		CacheDir:   t.TempDir(),
		Cwd:        cwd,
	}
	d := l.LoadOrDefaults(context.Background())
	if len(d.Allow) != 1 || d.Allow[0] != "cwd="+cwd {
		t.Errorf("stub did not receive cmd.Dir=cwd; got Allow=%v want [cwd=%s]", d.Allow, cwd)
	}
}

func TestLoad_SettingsRemovalInvalidates(t *testing.T) {
	cacheDir := t.TempDir()
	binary := installStub(t, sampleConfig)

	settingsPath := filepath.Join(t.TempDir(), "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	l := &Loader{
		BinaryPath:    binary,
		CacheDir:      cacheDir,
		Ttl:           time.Hour,
		SettingsPaths: []string{settingsPath},
	}
	_ = l.LoadOrDefaults(context.Background())

	if err := os.Remove(settingsPath); err != nil {
		t.Fatalf("remove settings: %v", err)
	}
	configPath := filepath.Join(filepath.Dir(binary), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"allow":["AFTER REMOVAL"]}`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	d := l.LoadOrDefaults(context.Background())
	if len(d.Allow) != 1 || d.Allow[0] != "AFTER REMOVAL" {
		t.Errorf("expected fresh fetch after settings removal, got %v", d.Allow)
	}
}

func TestLoad_SettingsMtimeInvalidatesCache(t *testing.T) {
	cacheDir := t.TempDir()
	binary := installStub(t, sampleConfig)

	settingsPath := filepath.Join(t.TempDir(), "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	l := &Loader{
		BinaryPath:    binary,
		CacheDir:      cacheDir,
		Ttl:           time.Hour,
		SettingsPaths: []string{settingsPath},
	}
	_ = l.LoadOrDefaults(context.Background())

	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(settingsPath, future, future); err != nil {
		t.Fatalf("touch: %v", err)
	}
	configPath := filepath.Join(filepath.Dir(binary), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"allow":["AFTER SETTINGS EDIT"]}`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	d := l.LoadOrDefaults(context.Background())
	if len(d.Allow) != 1 || d.Allow[0] != "AFTER SETTINGS EDIT" {
		t.Errorf("expected fresh fetch after settings.json mtime change, got %v", d.Allow)
	}
}

func TestLoad_TTLExpiryRefetches(t *testing.T) {
	cacheDir := t.TempDir()
	binary := installStub(t, sampleConfig)
	l := &Loader{
		BinaryPath: binary,
		CacheDir:   cacheDir,
		Ttl:        1 * time.Millisecond,
	}
	_ = l.LoadOrDefaults(context.Background())
	time.Sleep(10 * time.Millisecond)

	configPath := filepath.Join(filepath.Dir(binary), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"allow":["EXPIRED"]}`), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	d := l.LoadOrDefaults(context.Background())
	if len(d.Allow) != 1 || d.Allow[0] != "EXPIRED" {
		t.Errorf("expected fresh fetch after TTL, got %v", d.Allow)
	}
}

func TestLoad_BinaryMissing_FallsBackToBundled(t *testing.T) {
	l := &Loader{
		BinaryPath: filepath.Join(t.TempDir(), "no-such-claude"),
		CacheDir:   t.TempDir(),
	}
	p := l.LoadOrDefaults(context.Background())
	if len(p.Allow) == 0 || len(p.HardDeny) == 0 {
		t.Errorf("expected bundled defaults on failure, got empty policy: %+v", p)
	}
	assertIsBundledDefaults(t, p)
}

func TestLoad_InvalidJSONFromClaude_FallsBackToBundled(t *testing.T) {
	binary := installStub(t, "this is not json at all")
	l := &Loader{BinaryPath: binary, CacheDir: t.TempDir()}
	p := l.LoadOrDefaults(context.Background())
	assertIsBundledDefaults(t, p)
}

func TestBundledDefaults_NonEmpty(t *testing.T) {
	p := BundledDefaults()
	if len(p.Allow) == 0 {
		t.Error("BundledDefaults().Allow is empty")
	}
	if len(p.SoftDeny) == 0 {
		t.Error("BundledDefaults().SoftDeny is empty")
	}
	if len(p.HardDeny) == 0 {
		t.Error("BundledDefaults().HardDeny is empty")
	}
	if len(p.Environment) == 0 {
		t.Error("BundledDefaults().Environment is empty")
	}
}

func TestBundledDefaults_Idempotent(t *testing.T) {
	a := BundledDefaults()
	b := BundledDefaults()
	if len(a.Allow) != len(b.Allow) || a.Allow[0] != b.Allow[0] {
		t.Errorf("BundledDefaults() not stable across calls")
	}
}

func assertIsBundledDefaults(t *testing.T, p Policy) {
	t.Helper()
	bundled := BundledDefaults()
	if len(p.Allow) != len(bundled.Allow) {
		t.Errorf("Allow len=%d, want %d (bundled defaults)", len(p.Allow), len(bundled.Allow))
	}
	if len(p.HardDeny) != len(bundled.HardDeny) {
		t.Errorf("HardDeny len=%d, want %d (bundled defaults)", len(p.HardDeny), len(bundled.HardDeny))
	}
}

// TestRoundtrip_RealClaude smoke-tests against the user's installed `claude` binary. Only runs with
// CLAUDE_AUTO_PERMISSION_E2E=1.
func TestRoundtrip_RealClaude(t *testing.T) {
	if os.Getenv("CLAUDE_AUTO_PERMISSION_E2E") != "1" {
		t.Skip("set CLAUDE_AUTO_PERMISSION_E2E=1 to run real `claude` E2E")
	}
	l := &Loader{CacheDir: t.TempDir()}
	p := l.LoadOrDefaults(context.Background())
	if len(p.Allow) == 0 || len(p.HardDeny) == 0 {
		t.Errorf("expected non-empty allow/hard_deny from real `claude`; got %+v", p)
	}
	t.Logf("real `claude` policy: %d allow, %d soft_deny, %d hard_deny, %d environment",
		len(p.Allow), len(p.SoftDeny), len(p.HardDeny), len(p.Environment))

	// JSON-roundtrip sanity.
	if _, err := json.Marshal(p); err != nil {
		t.Errorf("Policy not JSON-encodable: %v", err)
	}
}
