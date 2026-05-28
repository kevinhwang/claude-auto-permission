package prompt

import (
	"encoding/json"

	cctranscript "claude-auto-permission/internal/claudecode/transcript"
)

// ToolInputSanitizer projects a tool's input into a smaller form for the classifier prompt (e.g., drop content body on
// Write). PassthroughSanitizer is the default for tools without one.
type ToolInputSanitizer func(toolName string, rawInput json.RawMessage) (json.RawMessage, error)

// PassthroughSanitizer returns rawInput unchanged.
func PassthroughSanitizer(_ string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return raw, nil
}

// Sanitize converts decoded transcript entries into classifier-safe Records:
//
//   - Real user prompts (content is a string, isMeta==false) → kept verbatim.
//   - Assistant tool_use blocks → kept; .input runs through sanitizeInput.
//   - Assistant text and thinking → dropped (model-authored, can influence the classifier).
//   - Synthetic-user (tool_result) entries → dropped (execution state is out of scope; matches reference
//     toCompactBlock).
//   - System-issued user messages (isMeta==true) → dropped (not real intent).
//   - System messages, attachments, queue-ops, *-title records → dropped (framework metadata).
//
// Tolerant by design: malformed individual records are skipped, not fatal — real transcripts have noise.
func Sanitize(entries []cctranscript.Entry, sanitizeInput ToolInputSanitizer) ([]Record, error) {
	if sanitizeInput == nil {
		sanitizeInput = PassthroughSanitizer
	}

	out := make([]Record, 0, len(entries))
	for _, e := range entries {
		recs, err := sanitizeEntry(e, sanitizeInput)
		if err != nil {
			continue
		}
		out = append(out, recs...)
	}
	return out, nil
}

func sanitizeEntry(e cctranscript.Entry, sanitizeInput ToolInputSanitizer) ([]Record, error) {
	switch e.Raw.Type {
	case "user":
		return sanitizeUserEntry(e)
	case "assistant":
		return sanitizeAssistantEntry(e, sanitizeInput)
	}
	// system / attachment / queue-operation / *-title / file-history are framework metadata.
	return nil, nil
}

func sanitizeUserEntry(e cctranscript.Entry) ([]Record, error) {
	if e.Raw.IsMeta {
		return nil, nil
	}
	// Real user prompts are JSON strings; tool_result entries are arrays and fall through to nil here, dropping them.
	text, ok := decodeString(e.Raw.Message.Content)
	if !ok {
		return nil, nil
	}
	return []Record{{
		Kind:     KindUser,
		User:     text,
		Uuid:     e.Raw.Uuid,
		Subagent: e.Source.SubagentType,
	}}, nil
}

// sanitizeAssistantEntry emits one KindCall record per tool_use block. text and thinking blocks are dropped.
func sanitizeAssistantEntry(e cctranscript.Entry, sanitizeInput ToolInputSanitizer) ([]Record, error) {
	blocks, err := decodeBlocks(e.Raw.Message.Content)
	if err != nil {
		return nil, err
	}

	var out []Record
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		input, err := sanitizeInput(b.Name, b.Input)
		if err != nil {
			// One broken sanitizer must not poison the whole transcript.
			input, _ = PassthroughSanitizer(b.Name, b.Input)
		}
		out = append(out, Record{
			Kind: KindCall,
			Call: &CallRecord{
				Tool:      b.Name,
				ToolInput: input,
			},
			Subagent: e.Source.SubagentType,
		})
	}
	return out, nil
}

func decodeString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	return "", false
}

// decodeBlocks parses content as an array of typed blocks. Non-array shapes return an empty slice without erroring —
// the caller moves on.
func decodeBlocks(raw json.RawMessage) ([]cctranscript.RawBlock, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var blocks []cctranscript.RawBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, nil
	}
	return blocks, nil
}
