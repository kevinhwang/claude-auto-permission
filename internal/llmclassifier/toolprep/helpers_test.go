package toolprep

import "encoding/json"

// jsonString JSON-encodes s as a JSON string literal. Test helper shared by every per-tool test file.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
