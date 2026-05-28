package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Record is one line in the classifier's view of the transcript.
//
// MarshalJSON produces the compact reference shape:
//   - KindUser → `{"user":"text"}`
//   - KindCall → `{"<ToolName>":<sanitized-input>}`
//
// Records sourced from a subagent's transcript get a `_subagent` key so the model can distinguish parent vs subagent
// activity.
type Record struct {
	Kind RecordKind `json:"-"`

	// User: the user's verbatim prompt text. KindUser only.
	User string `json:"-"`

	// Uuid identifies the source raw entry; carried only for KindUser so callers can key on the current user turn. Not
	// serialized.
	Uuid string `json:"-"`

	// Call: sanitized tool invocation. KindCall only.
	Call *CallRecord `json:"-"`

	// Subagent, when non-empty, names the subagent type the record came from.
	Subagent string `json:"-"`
}

// RecordKind names the variant a Record represents.
type RecordKind string

const (
	KindUser RecordKind = "user"
	KindCall RecordKind = "call"
)

// CallRecord describes one tool invocation. ToolInput is already sanitized via the per-tool Evaluator.Sanitize.
type CallRecord struct {
	Tool      string          `json:"tool"`
	ToolInput json.RawMessage `json:"input,omitempty"`
}

func (r Record) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	switch r.Kind {
	case KindUser:
		buf.WriteString(`"user":`)
		userBytes, err := json.Marshal(r.User)
		if err != nil {
			return nil, fmt.Errorf("marshal user: %w", err)
		}
		buf.Write(userBytes)
	case KindCall:
		if r.Call == nil {
			return nil, fmt.Errorf("KindCall record without Call payload")
		}
		nameBytes, err := json.Marshal(r.Call.Tool)
		if err != nil {
			return nil, fmt.Errorf("marshal tool name: %w", err)
		}
		buf.Write(nameBytes)
		buf.WriteByte(':')
		if len(r.Call.ToolInput) == 0 {
			buf.WriteString("\"\"")
		} else {
			buf.Write(r.Call.ToolInput)
		}
	default:
		return nil, fmt.Errorf("unknown record kind %q", r.Kind)
	}
	if r.Subagent != "" {
		buf.WriteString(`,"_subagent":`)
		subBytes, err := json.Marshal(r.Subagent)
		if err != nil {
			return nil, fmt.Errorf("marshal subagent: %w", err)
		}
		buf.Write(subBytes)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
