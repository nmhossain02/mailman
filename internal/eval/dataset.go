package eval

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/nmhossain02/mailman/internal/core"
)

// DatasetRecord is one portable JSONL case. InputReference is descriptive only;
// InputJSON remains embedded so a benchmark cannot change underneath a run.
type DatasetRecord struct {
	Case           core.EvalCase   `json:"case"`
	InputReference string          `json:"input_reference,omitempty"`
	ExpectedJSON   json.RawMessage `json:"expected_json,omitempty"`
	LabelSource    string          `json:"label_source,omitempty"`
}

func ReadJSONL(r io.Reader) ([]DatasetRecord, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var records []DatasetRecord
	for line := 1; scanner.Scan(); line++ {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var record DatasetRecord
		dec := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&record); err != nil {
			return nil, fmt.Errorf("eval JSONL line %d: %w", line, err)
		}
		if err := requireEOF(dec); err != nil {
			return nil, fmt.Errorf("eval JSONL line %d: %w", line, err)
		}
		if err := validateRecord(&record); err != nil {
			return nil, fmt.Errorf("eval JSONL line %d: %w", line, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read eval JSONL: %w", err)
	}
	return records, nil
}

func WriteJSONL(w io.Writer, records []DatasetRecord) error {
	copyRecords := append([]DatasetRecord(nil), records...)
	sort.SliceStable(copyRecords, func(i, j int) bool { return copyRecords[i].Case.ID < copyRecords[j].Case.ID })
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for i := range copyRecords {
		if err := validateRecord(&copyRecords[i]); err != nil {
			return fmt.Errorf("eval case %q: %w", copyRecords[i].Case.ID, err)
		}
		if err := enc.Encode(copyRecords[i]); err != nil {
			return fmt.Errorf("write eval JSONL: %w", err)
		}
	}
	return nil
}

func validateRecord(record *DatasetRecord) error {
	caseRecord := &record.Case
	if caseRecord.ID == "" || caseRecord.Dataset == "" || caseRecord.TaskName == "" || caseRecord.TaskVersion == "" {
		return errors.New("id, dataset, task name, and task version are required")
	}
	if !json.Valid(caseRecord.InputJSON) {
		return errors.New("input_json must be valid JSON")
	}
	if len(record.ExpectedJSON) > 0 && !json.Valid(record.ExpectedJSON) {
		return errors.New("expected_json must be valid JSON")
	}
	hash := inputHash(caseRecord.InputJSON)
	if caseRecord.InputHash == "" {
		caseRecord.InputHash = hash
	} else if caseRecord.InputHash != hash {
		return errors.New("input_hash does not match input_json")
	}
	return nil
}

func inputHash(input json.RawMessage) string {
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

func requireEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
