package output

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestSuccessJSONShapes(t *testing.T) {
	t.Parallel()

	target := validTarget()
	validated, err := NewValidated(target)
	if err != nil {
		t.Fatalf("NewValidated() error = %v", err)
	}
	var validatedOutput bytes.Buffer
	if err := WriteSuccess(&validatedOutput, validated, true); err != nil {
		t.Fatalf("WriteSuccess(validated) error = %v", err)
	}
	if !strings.Contains(validatedOutput.String(), `"schemaVersion":1`) ||
		!strings.Contains(validatedOutput.String(), `"status":"validated"`) ||
		!strings.Contains(validatedOutput.String(), `"suggestions":[`) ||
		!strings.Contains(validatedOutput.String(), `"posted":false`) ||
		strings.Contains(validatedOutput.String(), `"reviewID"`) {
		t.Fatalf("validated JSON = %s", validatedOutput.String())
	}

	created, err := NewCreated(target, 91, "https://github.com/octo/example/pull/42#pullrequestreview-91")
	if err != nil {
		t.Fatalf("NewCreated() error = %v", err)
	}
	var createdOutput bytes.Buffer
	if err := WriteSuccess(&createdOutput, created, true); err != nil {
		t.Fatalf("WriteSuccess(created) error = %v", err)
	}
	if !strings.Contains(createdOutput.String(), `"status":"created"`) ||
		!strings.Contains(createdOutput.String(), `"posted":true`) ||
		!strings.Contains(createdOutput.String(), `"reviewID":91`) {
		t.Fatalf("created JSON = %s", createdOutput.String())
	}
	for name, content := range map[string]string{
		"validated": validatedOutput.String(),
		"created":   createdOutput.String(),
	} {
		if !strings.HasSuffix(content, "\n") || strings.Count(content, "\n") != 1 {
			t.Errorf("%s JSON = %q, want exactly one trailing LF", name, content)
		}
		if strings.Contains(content, "replacement code") {
			t.Errorf("%s JSON exposed replacement content", name)
		}
	}
}

func TestSuccessConstructorsDefensivelyCopySuggestions(t *testing.T) {
	t.Parallel()

	target := validTarget()
	result, err := NewValidated(target)
	if err != nil {
		t.Fatalf("NewValidated() error = %v", err)
	}
	target.Suggestions[0].Path = "changed.go"
	if result.Suggestions[0].Path != "first.go" {
		t.Fatalf("result suggestions changed through caller slice: %#v", result.Suggestions)
	}
}

func TestSuccessValidationRejectsInvalidShapes(t *testing.T) {
	t.Parallel()

	base, err := NewValidated(validTarget())
	if err != nil {
		t.Fatalf("NewValidated() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Success)
	}{
		{name: "schema", mutate: func(value *Success) { value.SchemaVersion = 2 }},
		{name: "bad base SHA", mutate: func(value *Success) { value.BaseSHA = "bad" }},
		{name: "bad head SHA", mutate: func(value *Success) { value.HeadSHA = "bad" }},
		{name: "no suggestions", mutate: func(value *Success) { value.Suggestions = nil }},
		{name: "bad path", mutate: func(value *Success) { value.Suggestions[0].Path = "../x" }},
		{name: "bad range", mutate: func(value *Success) { value.Suggestions[0].Range.StartLine = 0 }},
		{name: "bad side", mutate: func(value *Success) { value.Suggestions[0].Range.Side = "LEFT" }},
		{name: "bad replacement hash", mutate: func(value *Success) { value.Suggestions[0].Replacement.SHA256 = "bad" }},
		{name: "bad body hash", mutate: func(value *Success) { value.Suggestions[0].BodySHA256 = "bad" }},
		{name: "bad review hash", mutate: func(value *Success) { value.ReviewBodySHA256 = "bad" }},
		{name: "bad request hash", mutate: func(value *Success) { value.RequestSHA256 = "bad" }},
		{name: "validated posted", mutate: func(value *Success) { value.Posted = true }},
		{name: "unknown status", mutate: func(value *Success) { value.Status = "other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			changed.Suggestions = cloneSuggestions(base.Suggestions)
			test.mutate(&changed)
			if err := changed.Validate(); !errors.Is(err, ErrInvalidSuccess) {
				t.Fatalf("Validate() error = %v, want ErrInvalidSuccess", err)
			}
		})
	}
}

func TestHumanSuccessSummarizesWholeReview(t *testing.T) {
	t.Parallel()

	validated, _ := NewValidated(validTarget())
	var output bytes.Buffer
	if err := WriteSuccess(&output, validated, false); err != nil {
		t.Fatalf("WriteSuccess() error = %v", err)
	}
	for _, expected := range []string{
		"Validated review for octo/example#42 with 2 suggestion(s)",
		"[1] first.go:7",
		"[2] second.go:10-12",
		"Request SHA-256:",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("human output %q does not contain %q", output.String(), expected)
		}
	}
}

func TestFailureOutputAndExitCodes(t *testing.T) {
	t.Parallel()

	details := map[string]any{"field": "reviewFile"}
	failure := NewFailure(domain.CodeInvalidArguments, "Invalid.", details)
	details["field"] = "changed"
	var output bytes.Buffer
	if err := WriteFailure(&output, failure, true); err != nil {
		t.Fatalf("WriteFailure() error = %v", err)
	}
	if output.String() != `{"schemaVersion":1,"error":{"code":"invalid_arguments","message":"Invalid.","details":{"field":"reviewFile"}}}`+"\n" {
		t.Fatalf("failure JSON = %q", output.String())
	}

	codes := map[domain.Code]int{
		domain.CodeInvalidArguments:     2,
		domain.CodeAuthenticationFailed: 3,
		domain.CodeTargetNotFound:       4,
		domain.CodeRangeNotCommentable:  5,
		domain.CodeStalePullRequest:     6,
		domain.CodeGitHubAPIError:       7,
		domain.CodeIOError:              8,
		domain.CodeRateLimited:          9,
		domain.CodeWriteOutcomeUnknown:  10,
		domain.CodeUnsupportedHost:      11,
		domain.CodeCancelled:            12,
		domain.CodePermissionDenied:     13,
	}
	for code, want := range codes {
		if got := ExitCode(code); got != want {
			t.Errorf("ExitCode(%q) = %d, want %d", code, got, want)
		}
	}
	if got := ExitCode("future"); got != 7 {
		t.Errorf("ExitCode(unknown) = %d, want 7", got)
	}
}

func TestWritersPropagateShortAndUnderlyingWrites(t *testing.T) {
	t.Parallel()

	success, _ := NewValidated(validTarget())
	if err := WriteSuccess(zeroWriter{}, success, true); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("WriteSuccess(zeroWriter) error = %v", err)
	}
	want := errors.New("write failed")
	if err := WriteFailure(errorWriter{err: want}, NewFailure(domain.CodeIOError, "x", nil), false); !errors.Is(err, want) {
		t.Fatalf("WriteFailure(errorWriter) error = %v", err)
	}
}

func validTarget() Target {
	hash := strings.Repeat("a", 64)
	return Target{
		Host:             "github.com",
		Repository:       "octo/example",
		PullRequest:      42,
		PullRequestURL:   "https://github.com/octo/example/pull/42",
		BaseSHA:          strings.Repeat("b", 40),
		HeadSHA:          strings.Repeat("c", 40),
		ReviewBodySHA256: hash,
		Suggestions: []Suggestion{
			{
				Path:        "first.go",
				Range:       Range{StartLine: 7, EndLine: 7, Side: domain.SideRight},
				Replacement: Replacement{ByteCount: 3, LineCount: 1, SHA256: hash},
				BodySHA256:  hash,
			},
			{
				Path:        "second.go",
				Range:       Range{StartLine: 10, EndLine: 12, Side: domain.SideRight},
				Replacement: Replacement{ByteCount: 7, LineCount: 2, SHA256: hash},
				BodySHA256:  hash,
			},
		},
		RequestSHA256: hash,
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

type errorWriter struct {
	err error
}

func (writer errorWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("wrapped: %w", writer.err)
}
