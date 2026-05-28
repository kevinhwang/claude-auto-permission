package llmclassifier

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-auto-permission/internal/llmclassifier/providers"
)

func TestMaybeDump_NoOpWhenDirEmpty(t *testing.T) {
	dir := t.TempDir()
	maybeDump("", "stub", providers.Request{SystemPrompt: "sys"}, providers.Result{}, nil)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read tmp: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dir should be empty when dumping disabled; got %v", entries)
	}
}

func TestMaybeDump_WritesRequestAndResponse(t *testing.T) {
	dumpDir := t.TempDir()

	req := providers.Request{
		SystemPrompt: "you are a classifier",
		UserPrompt:   "{\"Bash\":\"ls\"}\n",
		Schema:       json.RawMessage(`{"type":"object"}`),
	}
	res := providers.Result{ShouldBlock: false, Reason: "", LatencyMs: 250}

	maybeDump(dumpDir, "stub", req, res, nil)

	entries, err := os.ReadDir(dumpDir)
	if err != nil {
		t.Fatalf("read dump dir: %v", err)
	}
	var reqPath, resPath string
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".req.json"):
			reqPath = filepath.Join(dumpDir, e.Name())
		case strings.HasSuffix(e.Name(), ".res.json"):
			resPath = filepath.Join(dumpDir, e.Name())
		}
	}
	if reqPath == "" || resPath == "" {
		t.Fatalf("expected req+res dumps; got entries=%v", entries)
	}

	reqBytes, _ := os.ReadFile(reqPath)
	if !strings.Contains(string(reqBytes), "you are a classifier") {
		t.Errorf("req dump missing system prompt: %s", reqBytes)
	}
	if !strings.Contains(string(reqBytes), `"provider": "stub"`) {
		t.Errorf("req dump missing provider tag: %s", reqBytes)
	}

	resBytes, _ := os.ReadFile(resPath)
	if !strings.Contains(string(resBytes), `"latency_ms": 250`) {
		t.Errorf("res dump missing latency: %s", resBytes)
	}
}

func TestMaybeDump_RecordsErrorWhenProviderFailed(t *testing.T) {
	dumpDir := t.TempDir()
	maybeDump(dumpDir, "stub", providers.Request{}, providers.Result{}, errors.New("model timeout"))

	entries, _ := os.ReadDir(dumpDir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".res.json") {
			continue
		}
		blob, _ := os.ReadFile(filepath.Join(dumpDir, e.Name()))
		if !strings.Contains(string(blob), "model timeout") {
			t.Errorf("error not recorded in res dump: %s", blob)
		}
	}
}
