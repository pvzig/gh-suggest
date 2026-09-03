// Package attempt owns the non-sensitive recovery descriptor emitted after an
// ambiguous grouped-review write and consumed by read-only reconciliation.
package attempt

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/pvzig/gh-suggest/internal/jsoninput"
)

const (
	// DescriptorSchemaVersion identifies the ambiguous-attempt descriptor
	// shared by review creation and reconciliation.
	DescriptorSchemaVersion = 1
)

// Suggestion identifies one expected inline suggestion without exposing its
// Markdown body or replacement content.
type Suggestion struct {
	Path       string `json:"path"`
	StartLine  *int   `json:"startLine,omitempty"`
	EndLine    int    `json:"endLine"`
	Side       string `json:"side"`
	BodySHA256 string `json:"bodySHA256"`
}

// Descriptor is a complete, file-safe description of an ambiguous grouped
// review attempt. It contains only target coordinates and digests.
type Descriptor struct {
	SchemaVersion    int          `json:"schemaVersion"`
	Host             string       `json:"host"`
	Repository       string       `json:"repository"`
	PullRequest      int          `json:"pullRequest"`
	BaseSHA          string       `json:"baseSHA"`
	HeadSHA          string       `json:"headSHA"`
	Event            string       `json:"event"`
	ReviewBodySHA256 string       `json:"reviewBodySHA256"`
	Suggestions      []Suggestion `json:"suggestions"`
	RequestSHA256    string       `json:"requestSHA256"`
	AttemptStartedAt time.Time    `json:"attemptStartedAt"`
}

// DecodeDescriptor decodes the strict descriptor schema. Field names are
// case-sensitive, unknown and duplicate fields are rejected, and the input
// must contain exactly one object.
func DecodeDescriptor(content []byte) (Descriptor, error) {
	if err := jsoninput.RejectDuplicateFields(content); err != nil {
		return Descriptor{}, err
	}
	descriptorObject, err := jsoninput.ValidateObjectFields(
		content,
		[]string{
			"schemaVersion",
			"host",
			"repository",
			"pullRequest",
			"baseSHA",
			"headSHA",
			"event",
			"reviewBodySHA256",
			"suggestions",
			"requestSHA256",
			"attemptStartedAt",
		},
		false,
	)
	if err != nil {
		return Descriptor{}, err
	}

	if rawSuggestions, ok := descriptorObject["suggestions"]; ok {
		var suggestions []json.RawMessage
		if err := json.Unmarshal(rawSuggestions, &suggestions); err != nil {
			return Descriptor{}, err
		}
		for _, rawSuggestion := range suggestions {
			if _, err := jsoninput.ValidateObjectFields(
				rawSuggestion,
				[]string{"path", "startLine", "endLine", "side", "bodySHA256"},
				false,
			); err != nil {
				return Descriptor{}, err
			}
		}
	}

	var descriptor Descriptor
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return Descriptor{}, err
	}
	if err := jsoninput.RequireEOF(decoder); err != nil {
		return Descriptor{}, err
	}
	return descriptor, nil
}
