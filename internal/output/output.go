// Package output owns the stable process-output contract for gh-suggest.
package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/pvzig/gh-suggest/internal/domain"
)

const (
	// SchemaVersion is the version of the JSON success and error contracts.
	SchemaVersion = 1
)

// Status identifies which successful operation completed.
type Status string

const (
	// StatusValidated means a grouped review was validated without a write.
	StatusValidated Status = "validated"
	// StatusCreated means GitHub definitively returned the created review.
	StatusCreated Status = "created"
)

// Range is the inclusive, right-side line range represented by a suggestion.
type Range struct {
	StartLine int         `json:"startLine"`
	EndLine   int         `json:"endLine"`
	Side      domain.Side `json:"side"`
}

// Replacement summarizes normalized replacement text without exposing it.
type Replacement struct {
	ByteCount int    `json:"byteCount"`
	LineCount int    `json:"lineCount"`
	SHA256    string `json:"sha256"`
}

// Suggestion summarizes one ordered inline suggestion in a grouped review.
type Suggestion struct {
	Path        string      `json:"path"`
	Range       Range       `json:"range"`
	Replacement Replacement `json:"replacement"`
	BodySHA256  string      `json:"bodySHA256"`
}

// Target contains fields shared by validated and created review output.
type Target struct {
	Host             string
	Repository       string
	PullRequest      int
	PullRequestURL   string
	BaseSHA          string
	HeadSHA          string
	ReviewBodySHA256 string
	Suggestions      []Suggestion
	RequestSHA256    string
}

// Success is the versioned representation of grouped-review validation or
// creation.
type Success struct {
	SchemaVersion    int          `json:"schemaVersion"`
	Status           Status       `json:"status"`
	Host             string       `json:"host"`
	Repository       string       `json:"repository"`
	PullRequest      int          `json:"pullRequest"`
	PullRequestURL   string       `json:"pullRequestURL"`
	BaseSHA          string       `json:"baseSHA"`
	HeadSHA          string       `json:"headSHA"`
	ReviewBodySHA256 string       `json:"reviewBodySHA256"`
	Suggestions      []Suggestion `json:"suggestions"`
	RequestSHA256    string       `json:"requestSHA256"`
	Posted           bool         `json:"posted"`
	ReviewID         *int64       `json:"reviewID,omitempty"`
	URL              string       `json:"url,omitempty"`
}

// ErrInvalidSuccess indicates that Success violates the output contract.
var ErrInvalidSuccess = errors.New("invalid success output")

// NewValidated constructs dry-run output.
func NewValidated(target Target) (Success, error) {
	result := successFromTarget(target)
	result.Status = StatusValidated
	if err := result.Validate(); err != nil {
		return Success{}, err
	}
	return result, nil
}

// NewCreated constructs definitive grouped-review creation output.
func NewCreated(target Target, reviewID int64, url string) (Success, error) {
	reviewIDCopy := reviewID
	result := successFromTarget(target)
	result.Status = StatusCreated
	result.Posted = true
	result.ReviewID = &reviewIDCopy
	result.URL = url
	if err := result.Validate(); err != nil {
		return Success{}, err
	}
	return result, nil
}

func successFromTarget(target Target) Success {
	return Success{
		SchemaVersion:    SchemaVersion,
		Host:             target.Host,
		Repository:       target.Repository,
		PullRequest:      target.PullRequest,
		PullRequestURL:   target.PullRequestURL,
		BaseSHA:          target.BaseSHA,
		HeadSHA:          target.HeadSHA,
		ReviewBodySHA256: target.ReviewBodySHA256,
		Suggestions:      cloneSuggestions(target.Suggestions),
		RequestSHA256:    target.RequestSHA256,
	}
}

func cloneSuggestions(suggestions []Suggestion) []Suggestion {
	if suggestions == nil {
		return nil
	}
	cloned := make([]Suggestion, len(suggestions))
	copy(cloned, suggestions)
	return cloned
}

// Validate verifies the structural invariants of the success contract.
func (success Success) Validate() error {
	if success.SchemaVersion != SchemaVersion {
		return invalidSuccess("schemaVersion must be %d", SchemaVersion)
	}
	if success.Host == "" ||
		success.Repository == "" ||
		success.PullRequestURL == "" {
		return invalidSuccess("resolved pull-request fields are required")
	}
	if !domain.ValidCommitSHA(success.BaseSHA) ||
		!domain.ValidCommitSHA(success.HeadSHA) {
		return invalidSuccess("baseSHA and headSHA must be full commit SHAs")
	}
	if success.PullRequest <= 0 {
		return invalidSuccess("pullRequest must be positive")
	}
	if !domain.ValidSHA256Hex(success.ReviewBodySHA256) {
		return invalidSuccess("reviewBodySHA256 must be a SHA-256 digest")
	}
	if !domain.ValidSHA256Hex(success.RequestSHA256) {
		return invalidSuccess("requestSHA256 must be a SHA-256 digest")
	}
	if len(success.Suggestions) == 0 {
		return invalidSuccess("at least one suggestion is required")
	}
	for index, suggestion := range success.Suggestions {
		if !domain.ValidRepositoryPath(suggestion.Path) {
			return invalidSuccess("suggestions[%d].path is invalid", index)
		}
		if suggestion.Range.StartLine <= 0 ||
			suggestion.Range.EndLine < suggestion.Range.StartLine ||
			suggestion.Range.Side != domain.SideRight {
			return invalidSuccess("suggestions[%d].range is invalid", index)
		}
		if suggestion.Replacement.ByteCount < 0 ||
			suggestion.Replacement.LineCount < 0 ||
			!domain.ValidSHA256Hex(suggestion.Replacement.SHA256) {
			return invalidSuccess("suggestions[%d].replacement is invalid", index)
		}
		if !domain.ValidSHA256Hex(suggestion.BodySHA256) {
			return invalidSuccess("suggestions[%d].bodySHA256 is invalid", index)
		}
	}

	switch success.Status {
	case StatusValidated:
		if success.Posted || success.ReviewID != nil || success.URL != "" {
			return invalidSuccess("validated output cannot contain created-review fields")
		}
	case StatusCreated:
		if !success.Posted ||
			success.ReviewID == nil ||
			*success.ReviewID <= 0 ||
			success.URL == "" {
			return invalidSuccess("created output requires posted, reviewID, and url")
		}
	default:
		return invalidSuccess("unrecognized status %q", success.Status)
	}
	return nil
}

func invalidSuccess(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSuccess, fmt.Sprintf(format, arguments...))
}

// ExitCode returns the process exit code associated with code. Unknown values
// conservatively fall back to github_api_error.
func ExitCode(code domain.Code) int {
	switch code {
	case domain.CodeInvalidArguments:
		return 2
	case domain.CodeAuthenticationFailed:
		return 3
	case domain.CodeTargetNotFound:
		return 4
	case domain.CodeRangeNotCommentable:
		return 5
	case domain.CodeStalePullRequest:
		return 6
	case domain.CodeGitHubAPIError:
		return 7
	case domain.CodeIOError:
		return 8
	case domain.CodeRateLimited:
		return 9
	case domain.CodeWriteOutcomeUnknown:
		return 10
	case domain.CodeUnsupportedHost:
		return 11
	case domain.CodeCancelled:
		return 12
	case domain.CodePermissionDenied:
		return 13
	default:
		return 7
	}
}

// Failure is the stable inner error object.
type Failure struct {
	Code    domain.Code    `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// NewFailure constructs a failure and shallow-copies its details map.
func NewFailure(code domain.Code, message string, details map[string]any) Failure {
	if !code.Known() {
		code = domain.CodeGitHubAPIError
	}
	if message == "" {
		message = "The operation failed."
	}
	return Failure{
		Code:    code,
		Message: message,
		Details: domain.CloneDetails(details),
	}
}

func (failure Failure) normalized() Failure {
	return NewFailure(failure.Code, failure.Message, failure.Details)
}

// Error returns the human-readable failure message.
func (failure Failure) Error() string {
	return failure.Message
}

// ExitCode returns this failure's stable process exit code.
func (failure Failure) ExitCode() int {
	return ExitCode(failure.Code)
}

// ErrorEnvelope is the versioned JSON failure representation.
type ErrorEnvelope struct {
	SchemaVersion int     `json:"schemaVersion"`
	Error         Failure `json:"error"`
}

// NewErrorEnvelope constructs a valid error envelope.
func NewErrorEnvelope(failure Failure) ErrorEnvelope {
	return ErrorEnvelope{
		SchemaVersion: SchemaVersion,
		Error:         failure.normalized(),
	}
}

// WriteSuccess emits stable JSON or concise human output.
func WriteSuccess(writer io.Writer, success Success, jsonMode bool) error {
	if err := success.Validate(); err != nil {
		return err
	}
	if jsonMode {
		return writeJSON(writer, success)
	}
	return writeHumanSuccess(writer, success)
}

// WriteFailure emits stable JSON or concise human output.
func WriteFailure(writer io.Writer, failure Failure, jsonMode bool) error {
	if jsonMode {
		return writeJSON(writer, NewErrorEnvelope(failure))
	}
	return writeHumanFailure(writer, failure.normalized())
}

func writeJSON(writer io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}
	return writeAll(writer, append(encoded, '\n'))
}

func writeHumanSuccess(writer io.Writer, success Success) error {
	var output bytes.Buffer
	switch success.Status {
	case StatusValidated:
		fmt.Fprintf(
			&output,
			"Validated review for %s#%d with %d suggestion(s); no review was posted.\n",
			success.Repository,
			success.PullRequest,
			len(success.Suggestions),
		)
	case StatusCreated:
		fmt.Fprintf(
			&output,
			"Created review for %s#%d with %d suggestion(s).\n",
			success.Repository,
			success.PullRequest,
			len(success.Suggestions),
		)
		fmt.Fprintf(&output, "Review: %s (ID %d)\n", success.URL, *success.ReviewID)
	}
	fmt.Fprintf(&output, "Head SHA: %s\n", success.HeadSHA)
	for index, suggestion := range success.Suggestions {
		lineRange := fmt.Sprintf("%d", suggestion.Range.StartLine)
		if suggestion.Range.StartLine != suggestion.Range.EndLine {
			lineRange = fmt.Sprintf(
				"%d-%d",
				suggestion.Range.StartLine,
				suggestion.Range.EndLine,
			)
		}
		fmt.Fprintf(
			&output,
			"[%d] %s:%s replacement SHA-256: %s\n",
			index+1,
			suggestion.Path,
			lineRange,
			suggestion.Replacement.SHA256,
		)
	}
	fmt.Fprintf(&output, "Request SHA-256: %s\n", success.RequestSHA256)
	return writeAll(writer, output.Bytes())
}

func writeHumanFailure(writer io.Writer, failure Failure) error {
	var output bytes.Buffer
	fmt.Fprintf(&output, "error [%s]: %s\n", failure.Code, failure.Message)
	if len(failure.Details) > 0 {
		encoded, err := json.Marshal(failure.Details)
		if err != nil {
			return fmt.Errorf("encode error details: %w", err)
		}
		fmt.Fprintf(&output, "details: %s\n", encoded)
	}
	return writeAll(writer, output.Bytes())
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
