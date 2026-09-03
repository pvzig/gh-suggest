package output

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/pvzig/gh-suggest/internal/domain"
)

// Reconciliation is the stable whole-review read-only result.
type Reconciliation struct {
	SchemaVersion     int                         `json:"schemaVersion"`
	Status            domain.ReconciliationStatus `json:"status"`
	Host              string                      `json:"host"`
	Repository        string                      `json:"repository"`
	PullRequest       int                         `json:"pullRequest"`
	HeadSHA           string                      `json:"headSHA"`
	RequestSHA256     string                      `json:"requestSHA256"`
	AttemptStartedAt  string                      `json:"attemptStartedAt"`
	SuggestionCount   int                         `json:"suggestionCount"`
	CandidateCount    int                         `json:"candidateCount"`
	PartialMatchCount int                         `json:"partialMatchCount"`
	MatchCount        int                         `json:"matchCount"`
	ReviewID          *int64                      `json:"reviewID,omitempty"`
	URL               string                      `json:"url,omitempty"`
}

// ErrInvalidReconciliation indicates an invalid reconciliation output shape.
var ErrInvalidReconciliation = errors.New("invalid reconciliation output")

// Validate verifies the reconciliation contract.
func (result Reconciliation) Validate() error {
	if result.SchemaVersion != SchemaVersion ||
		result.Host == "" ||
		result.Repository == "" ||
		result.PullRequest <= 0 ||
		!domain.ValidCommitSHA(result.HeadSHA) ||
		!domain.ValidSHA256Hex(result.RequestSHA256) ||
		result.AttemptStartedAt == "" ||
		result.SuggestionCount <= 0 ||
		result.CandidateCount < 0 ||
		result.PartialMatchCount < 0 ||
		result.MatchCount < 0 ||
		result.PartialMatchCount+result.MatchCount > result.CandidateCount {
		return fmt.Errorf(
			"%w: required review fields are invalid",
			ErrInvalidReconciliation,
		)
	}
	switch result.Status {
	case domain.ReconciliationLikelyCreated:
		if result.MatchCount != 1 ||
			result.ReviewID == nil ||
			*result.ReviewID <= 0 ||
			result.URL == "" {
			return fmt.Errorf(
				"%w: likely_created requires one review ID and URL",
				ErrInvalidReconciliation,
			)
		}
	case domain.ReconciliationUnknown:
		if result.MatchCount == 1 || result.ReviewID != nil || result.URL != "" {
			return fmt.Errorf(
				"%w: unknown requires zero or multiple exact reviews",
				ErrInvalidReconciliation,
			)
		}
	default:
		return fmt.Errorf(
			"%w: unknown status %q",
			ErrInvalidReconciliation,
			result.Status,
		)
	}
	return nil
}

// WriteReconciliation emits JSON or a concise human summary.
func WriteReconciliation(
	writer io.Writer,
	result Reconciliation,
	jsonMode bool,
) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if jsonMode {
		return writeJSON(writer, result)
	}

	var content bytes.Buffer
	if result.Status == domain.ReconciliationLikelyCreated {
		fmt.Fprintf(
			&content,
			"Found one exact review that was likely created: %s (ID %d).\n",
			result.URL,
			*result.ReviewID,
		)
	} else {
		fmt.Fprintf(
			&content,
			"Write outcome remains unknown; found %d exact and %d partial review match(es).\n",
			result.MatchCount,
			result.PartialMatchCount,
		)
	}
	return writeAll(writer, content.Bytes())
}
