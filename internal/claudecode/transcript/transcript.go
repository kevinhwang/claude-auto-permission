// Package transcript reads Claude Code session JSONL files into a minimal decoded form. The wire shape is fixed by
// Claude Code and used for every consumer of session history; the projection from raw entries to consumer-specific
// record shapes is left to callers.
//
// Two paths the LLM classifier reads:
//
//   - The parent transcript at the path Claude Code supplies in the hook event.
//   - The subagent transcript when the event is for a subagent —
//     `~/.claude/projects/<slug>/<parentSessionID>/subagents/agent-<agentId>.jsonl`.
//
// Hook docs: https://code.claude.com/docs/en/hooks.
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Entry is one decoded transcript line plus its source. `Raw` carries only the fields any consumer might want;
// tool-result payloads, full assistant prose, etc. are left in `Message.Content` for context-specific decoding.
type Entry struct {
	Raw    RawEntry
	Source Source
}

// RawEntry is the on-disk JSONL line shape.
type RawEntry struct {
	Type        string `json:"type"`
	Uuid        string `json:"uuid"`
	ParentUuid  string `json:"parentUuid"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`

	Message RawMessage `json:"message"`
}

// RawMessage is the Anthropic message envelope. `Content` is either a string (real user prompt) or an array of typed
// blocks; consumers re-parse it by context.
type RawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// RawBlock is one element of an assistant content array. `tool_result` inner fields aren't decoded — they're noise for
// any current consumer.
type RawBlock struct {
	Type string `json:"type"`

	// Tool-use blocks.
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// Text blocks — read only to discard.
	Text string `json:"text"`
}

// Source identifies one transcript file. `SubagentType`, when non-empty, tags entries so consumers can distinguish
// parent vs subagent activity.
type Source struct {
	Path         string
	SubagentType string
}

// scanLineMax is the max JSONL line size. Claude Code transcript records can embed full tool results, so the bufio
// scanner needs a generous buffer.
const scanLineMax = 4 * 1024 * 1024

// ReadAll reads JSONL lines from every source in order. Malformed individual lines are skipped — transcripts can be
// truncated mid-write and one bad line shouldn't drop the rest.
func ReadAll(sources ...Source) ([]Entry, error) {
	var out []Entry
	for _, src := range sources {
		entries, err := readFile(src)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}

func readFile(src Source) ([]Entry, error) {
	f, err := os.Open(src.Path)
	if err != nil {
		return nil, fmt.Errorf("open transcript %s: %w", src.Path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), scanLineMax)

	var entries []Entry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r RawEntry
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		entries = append(entries, Entry{Raw: r, Source: src})
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("scan transcript %s: %w", src.Path, err)
	}
	return entries, nil
}

// LocatePath echoes the `transcript_path` field from a hook event. Empty input returns empty (caller proceeds with no
// history) rather than guessing a default path.
func LocatePath(transcriptPath string) string {
	return transcriptPath
}

// SubagentTranscriptPath derives the canonical subagent transcript path from a parent transcript path plus the
// subagent's ID. Used as a fallback when `agent_transcript_path` isn't present on the hook input.
func SubagentTranscriptPath(parentTranscriptPath, agentId string) string {
	if parentTranscriptPath == "" || agentId == "" {
		return ""
	}
	dir := filepath.Dir(parentTranscriptPath)
	parentSession := strings.TrimSuffix(filepath.Base(parentTranscriptPath), ".jsonl")
	return filepath.Join(dir, parentSession, "subagents", "agent-"+agentId+".jsonl")
}
