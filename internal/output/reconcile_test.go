package output

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestReconciliationOutputShapes(t *testing.T) {
	t.Parallel()

	likely := validReconciliation()
	var jsonOutput bytes.Buffer
	if err := WriteReconciliation(&jsonOutput, likely, true); err != nil {
		t.Fatalf("WriteReconciliation() error = %v", err)
	}
	if !strings.Contains(jsonOutput.String(), `"status":"likely_created"`) ||
		!strings.Contains(jsonOutput.String(), `"reviewID":99`) ||
		!strings.Contains(jsonOutput.String(), `"suggestionCount":2`) {
		t.Fatalf("reconciliation JSON = %s", jsonOutput.String())
	}

	unknown := likely
	unknown.Status = domain.ReconciliationUnknown
	unknown.MatchCount = 0
	unknown.PartialMatchCount = 1
	unknown.ReviewID = nil
	unknown.URL = ""
	var humanOutput bytes.Buffer
	if err := WriteReconciliation(&humanOutput, unknown, false); err != nil {
		t.Fatalf("WriteReconciliation(unknown) error = %v", err)
	}
	if !strings.Contains(humanOutput.String(), "0 exact and 1 partial") {
		t.Fatalf("human reconciliation = %q", humanOutput.String())
	}
}

func TestReconciliationValidation(t *testing.T) {
	t.Parallel()

	base := validReconciliation()
	tests := []struct {
		name   string
		mutate func(*Reconciliation)
	}{
		{name: "schema", mutate: func(value *Reconciliation) { value.SchemaVersion = 2 }},
		{name: "head", mutate: func(value *Reconciliation) { value.HeadSHA = "bad" }},
		{name: "request", mutate: func(value *Reconciliation) { value.RequestSHA256 = "bad" }},
		{name: "suggestions", mutate: func(value *Reconciliation) { value.SuggestionCount = 0 }},
		{name: "counts", mutate: func(value *Reconciliation) { value.PartialMatchCount = 2 }},
		{name: "likely count", mutate: func(value *Reconciliation) { value.MatchCount = 2 }},
		{name: "likely id", mutate: func(value *Reconciliation) { value.ReviewID = nil }},
		{name: "unknown one match", mutate: func(value *Reconciliation) {
			value.Status = domain.ReconciliationUnknown
			value.ReviewID = nil
			value.URL = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			if err := changed.Validate(); !errors.Is(err, ErrInvalidReconciliation) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func validReconciliation() Reconciliation {
	reviewID := int64(99)
	return Reconciliation{
		SchemaVersion:    SchemaVersion,
		Status:           domain.ReconciliationLikelyCreated,
		Host:             "github.com",
		Repository:       "octo/example",
		PullRequest:      42,
		HeadSHA:          strings.Repeat("a", 40),
		RequestSHA256:    strings.Repeat("b", 64),
		AttemptStartedAt: "2026-07-27T12:00:00Z",
		SuggestionCount:  2,
		CandidateCount:   1,
		MatchCount:       1,
		ReviewID:         &reviewID,
		URL:              "https://github.com/octo/example/pull/42#pullrequestreview-99",
	}
}
