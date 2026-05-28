// Package automodepolicy loads the auto-mode policy the classifier prompt renders into its three-tier rule structure
// (Allow / SoftDeny / HardDeny / Environment).
//
// Loader shells out to `claude auto-mode config`, which merges shipped defaults with user autoMode.* overrides from the
// trusted settings hierarchy (user + local + managed — project-tier excluded to prevent hostile-repo policy injection).
// Results cache on disk keyed by (binary stat, settings-file stats), with a 24h Ttl backstop.
package automodepolicy

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"go.yaml.in/yaml/v4"

	"claude-auto-permission/internal/logging"
)

// Policy is the parsed auto-mode policy rendered into the classifier prompt.
type Policy struct {
	Allow       []string `json:"allow" yaml:"allow"`
	SoftDeny    []string `json:"soft_deny" yaml:"soft_deny"`
	HardDeny    []string `json:"hard_deny" yaml:"hard_deny"`
	Environment []string `json:"environment" yaml:"environment"`
}

//go:embed defaults.yaml
var defaultYaml []byte

var bundledDefaults = sync.OnceValue(func() Policy {
	var p Policy
	if err := yaml.Unmarshal(defaultYaml, &p); err != nil {
		panic("automodepolicy: parse bundled defaults.yaml: " + err.Error())
	}
	return p
})

// BundledDefaults returns the compile-time embedded policy defaults. Used as a fallback when the `claude` binary is
// unavailable and no valid cache exists.
func BundledDefaults() Policy { return bundledDefaults() }

// Loader reads, caches, and returns Policy.
type Loader struct {
	BinaryPath    string // empty → exec.LookPath("claude")
	CacheDir      string // required
	Ttl           time.Duration
	SettingsPaths []string // ordered; non-existent paths still keyed by string
	Cwd           string   // cmd.Dir on cache miss
}

// LoadOrDefaults returns the merged auto-mode policy, falling back to [BundledDefaults] on any error along the way
// (binary missing, shell-out failure, parse error, cache I/O). Errors are logged via the request-scoped logger from
// `ctx` so a misconfigured `claude` binary doesn't read as silent success.
//
// On a cache miss it shells out to `claude` with `cmd.Dir=l.Cwd` so `settings.local.json` resolves to the right
// project.
func (l *Loader) LoadOrDefaults(ctx context.Context) Policy {
	log := logging.FromContext(ctx)

	binary, realBinary, binaryInfo, err := l.resolveBinary()
	if err != nil {
		log.Warn("auto-mode policy: `claude` binary unavailable; using bundled defaults", "err", err)
		return BundledDefaults()
	}

	key := buildCacheKey(realBinary, binaryInfo, l.SettingsPaths)
	cachePath, err := l.cachePath(key)
	if err != nil {
		log.Warn("auto-mode policy: cache path setup failed; using bundled defaults", "err", err)
		return BundledDefaults()
	}

	if p, ok := l.readFreshCache(cachePath); ok {
		return p
	}

	p, err := fetchFromClaude(ctx, binary, l.Cwd)
	if err != nil {
		log.Warn("auto-mode policy: `claude` shell-out failed; using bundled defaults", "err", err)
		return BundledDefaults()
	}

	if werr := l.writeCache(cachePath, p); werr != nil {
		// Cache write is best-effort: the next call will just shell out again. Log so an unwritable cache dir doesn't go
		// undetected forever.
		log.Warn("auto-mode policy: cache write failed", "err", werr)
	}
	return p
}

// resolveBinary returns (path-to-invoke, realpath, stat). The realpath and stat go into the cache key.
func (l *Loader) resolveBinary() (invoke, realPath string, info os.FileInfo, err error) {
	invoke = l.BinaryPath
	if invoke == "" {
		invoke = "claude"
	}
	resolved, err := exec.LookPath(invoke)
	if err != nil {
		return "", "", nil, fmt.Errorf("`claude` binary %q not found: %w", invoke, err)
	}
	realPath, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", "", nil, fmt.Errorf("`claude` binary %q not found: %w", invoke, err)
	}
	info, err = os.Stat(realPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("`claude` binary %q not found: %w", invoke, err)
	}
	return invoke, realPath, info, nil
}

type binaryFP struct {
	RealPath string
	Size     int64
	MtimeNs  int64
}

type settingsFP struct {
	Path    string
	Exists  bool
	Size    int64
	MtimeNs int64
}

type cacheKey struct {
	Subcommand string
	Binary     binaryFP
	Settings   []settingsFP
}

// writeTo writes a deterministic byte stream: NUL-separated strings + big-endian int64s. The Settings slice is
// length-prefixed so distinct configurations can't collapse to identical bytes.
func (k cacheKey) writeTo(w io.Writer) {
	writeStr := func(s string) {
		_, _ = io.WriteString(w, s)
		_, _ = w.Write([]byte{0})
	}
	writeI64 := func(n int64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		_, _ = w.Write(b[:])
		_, _ = w.Write([]byte{0})
	}
	writeStr(k.Subcommand)
	writeStr(k.Binary.RealPath)
	writeI64(k.Binary.Size)
	writeI64(k.Binary.MtimeNs)
	writeI64(int64(len(k.Settings)))
	for _, s := range k.Settings {
		writeStr(s.Path)
		if s.Exists {
			writeStr("E")
		} else {
			writeStr("M")
		}
		writeI64(s.Size)
		writeI64(s.MtimeNs)
	}
}

func buildCacheKey(realPath string, info os.FileInfo, settingsPaths []string) cacheKey {
	k := cacheKey{
		Subcommand: "config",
		Binary: binaryFP{
			RealPath: realPath,
			Size:     info.Size(),
			MtimeNs:  info.ModTime().UnixNano(),
		},
		Settings: make([]settingsFP, 0, len(settingsPaths)),
	}
	for _, p := range settingsPaths {
		fp := settingsFP{Path: p}
		if si, err := os.Stat(p); err == nil {
			fp.Exists = true
			fp.Size = si.Size()
			fp.MtimeNs = si.ModTime().UnixNano()
		} else if !errors.Is(err, fs.ErrNotExist) {
			// Stat errors other than not-exist (permission etc.) leave Exists=false; the path string still keeps the key
			// cwd-distinct, and the shell-out will surface the real problem.
		}
		k.Settings = append(k.Settings, fp)
	}
	return k
}

func (l *Loader) cachePath(k cacheKey) (string, error) {
	dir := l.CacheDir
	if dir == "" {
		return "", fmt.Errorf("CacheDir is empty; set it before Load")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	h := sha256.New()
	k.writeTo(h)
	return filepath.Join(dir, hex.EncodeToString(h.Sum(nil))+".json"), nil
}

// cacheEntry is the on-disk shape — Policy plus stored-at for Ttl.
type cacheEntry struct {
	StoredAt time.Time `json:"stored_at"`
	Policy   Policy    `json:"policy"`
}

func (l *Loader) readFreshCache(path string) (Policy, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, false
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return Policy{}, false
	}
	if time.Since(e.StoredAt) > l.Ttl {
		return Policy{}, false
	}
	return e.Policy, true
}

func (l *Loader) writeCache(path string, p Policy) error {
	entry := cacheEntry{StoredAt: time.Now(), Policy: p}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// fetchFromClaude shells out to `claude auto-mode config` from cwd so settings.local.json resolves to the right
// project.
func fetchFromClaude(ctx context.Context, binary, cwd string) (Policy, error) {
	cmd := exec.CommandContext(ctx, binary, "auto-mode", "config")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr = string(ee.Stderr)
		}
		return Policy{}, fmt.Errorf("`claude auto-mode config`: %w (stderr=%s)",
			err, truncate(stderr, 500))
	}
	var p Policy
	if err := json.Unmarshal(out, &p); err != nil {
		return Policy{}, fmt.Errorf("parse auto-mode config output: %w", err)
	}
	return p, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
