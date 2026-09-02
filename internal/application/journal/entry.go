package journal

import (
	"encoding/json"
	"time"
)

type Entry struct {
	ExecutionKey, RequestHash, State string
	Response                         json.RawMessage
	UpdatedAt                        time.Time
}
