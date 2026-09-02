package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// InputHash hashes a compact JSON encoding so insignificant whitespace does
// not split equivalent cache/eval cases.
func InputHash(input json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return "", fmt.Errorf("normalize inference input: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func CacheKey(backendID, modelRevision string, task Task, input json.RawMessage) (string, error) {
	inputHash, err := InputHash(input)
	if err != nil {
		return "", err
	}
	material, _ := json.Marshal([]string{backendID, modelRevision, task.Name, task.Version, task.PromptVersion, task.SchemaVersion, inputHash})
	sum := sha256.Sum256(material)
	return hex.EncodeToString(sum[:]), nil
}
