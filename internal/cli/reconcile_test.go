package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pvzig/gh-suggest/internal/attempt"
	"github.com/pvzig/gh-suggest/internal/create"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/output"
	"github.com/pvzig/gh-suggest/internal/reconcile"
)

func TestRunReconcileLoadsAmbiguousErrorEnvelope(t *testing.T) {
	t.Parallel()

	attemptPath := writeAttemptFixture(t, validAttemptEnvelope())
	reconciler := &recordingReconciler{result: likelyReconciliationResult()}
	result := runCLI(
		&recordingCreator{},
		reconciler,
		[]string{"reconcile", "--attempt-file", attemptPath, "--json"},
		"",
	)

	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("run = %#v", result)
	}
	if !strings.Contains(result.stdout, `"status":"likely_created"`) ||
		!strings.Contains(result.stdout, `"reviewID":99`) {
		t.Fatalf("stdout = %q", result.stdout)
	}
	if len(reconciler.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(reconciler.requests))
	}
	descriptor := reconciler.requests[0].Descriptor
	if descriptor.Repository != "octo/example" ||
		descriptor.PullRequest != 42 ||
		len(descriptor.Suggestions) != 1 ||
		descriptor.Suggestions[0].Path != "first.go" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
}

func TestRunReconcileAcceptsAttemptEnvelopeOnStandardInput(t *testing.T) {
	t.Parallel()

	reconciler := &recordingReconciler{result: likelyReconciliationResult()}
	result := runCLI(
		&recordingCreator{},
		reconciler,
		[]string{"reconcile", "--attempt-file", "-", "--json"},
		validAttemptEnvelope(),
	)
	if result.exitCode != 0 {
		t.Fatalf("run = %#v", result)
	}
}

func TestRunReconcileReportsOutputWriteFailureAsIOError(t *testing.T) {
	t.Parallel()

	attemptPath := writeAttemptFixture(t, validAttemptEnvelope())
	var stderr strings.Builder
	exitCode := New(
		&recordingCreator{},
		&recordingReconciler{result: likelyReconciliationResult()},
	).Run(
		context.Background(),
		[]string{"reconcile", "--attempt-file", attemptPath, "--json"},
		strings.NewReader(""),
		errorWriter{err: errors.New("stdout is closed")},
		&stderr,
	)

	if exitCode != 8 ||
		!strings.Contains(stderr.String(), `"code":"io_error"`) ||
		!strings.Contains(stderr.String(), "could not be written") {
		t.Fatalf("exit code = %d; stderr = %q", exitCode, stderr.String())
	}
}

func TestRunReconcileAcceptsAdditiveOuterEnvelopeFields(t *testing.T) {
	t.Parallel()

	var envelope map[string]any
	if err := json.Unmarshal([]byte(validAttemptEnvelope()), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["traceID"] = "local-diagnostic"
	errorObject := envelope["error"].(map[string]any)
	errorObject["futureErrorField"] = true
	details := errorObject["details"].(map[string]any)
	details["futureDetail"] = "ignored"
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	reconciler := &recordingReconciler{result: likelyReconciliationResult()}
	result := runCLI(
		&recordingCreator{},
		reconciler,
		[]string{"reconcile", "--attempt-file", "-", "--json"},
		string(encoded),
	)
	if result.exitCode != 0 || len(reconciler.requests) != 1 {
		t.Fatalf("run = %#v; requests = %d", result, len(reconciler.requests))
	}
}

func TestParseAttemptFileAcceptsEmittedAmbiguousReviewFailure(t *testing.T) {
	t.Parallel()

	attemptStartedAt := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	writeRequest := create.ReviewRequest{
		Ref: domain.PullRequestRef{
			Repository: domain.Repository{
				Host:  "github.com",
				Owner: "octo",
				Name:  "example",
			},
			Number: 42,
		},
		BaseSHA: strings.Repeat("a", 40),
		HeadSHA: strings.Repeat("b", 40),
		Event:   domain.ReviewEventComment,
		Body:    "One focused review.",
		Comments: []create.ReviewComment{
			{
				Body:    "```suggestion\nfixed\n```",
				Path:    "first.go",
				EndLine: 7,
			},
		},
		RequestSHA256: strings.Repeat("e", 64),
	}
	ambiguous := create.NewAmbiguousWriteFailure(
		writeRequest,
		attemptStartedAt,
		"Unknown.",
		errors.New("connection reset"),
	)
	var encoded bytes.Buffer
	if err := output.WriteFailure(
		&encoded,
		output.NewFailure(ambiguous.Code, ambiguous.Message, ambiguous.Details),
		true,
	); err != nil {
		t.Fatalf("WriteFailure() error = %v", err)
	}

	path := writeAttemptFixture(t, encoded.String())
	request, failure := parseAttemptFile(path, strings.NewReader(""))
	if failure != nil {
		t.Fatalf("parseAttemptFile() failure = %v", failure)
	}
	descriptor := request.Descriptor
	if descriptor.Repository != "octo/example" ||
		descriptor.PullRequest != 42 ||
		descriptor.AttemptStartedAt != attemptStartedAt ||
		len(descriptor.Suggestions) != 1 ||
		descriptor.Suggestions[0].Path != "first.go" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
}

func TestRunReconcileRejectsInvalidAttemptFiles(t *testing.T) {
	t.Parallel()

	validDescriptor, _ := json.Marshal(validDescriptor())
	tests := []struct {
		name    string
		content string
		message string
	}{
		{
			name:    "not JSON",
			content: `{`,
			message: "not a JSON error envelope",
		},
		{
			name: "wrong envelope schema",
			content: `{"schemaVersion":2,"error":{"code":"write_outcome_unknown",` +
				`"details":{"reconciliation":` + string(validDescriptor) + `}}}`,
			message: "schemaVersion must be 1",
		},
		{
			name: "wrong error",
			content: `{"schemaVersion":1,"error":{"code":"invalid_arguments",` +
				`"details":{"reconciliation":` + string(validDescriptor) + `}}}`,
			message: "must contain write_outcome_unknown",
		},
		{
			name: "missing descriptor",
			content: `{"schemaVersion":1,"error":{"code":"write_outcome_unknown",` +
				`"details":{}}}`,
			message: "must contain write_outcome_unknown",
		},
		{
			name: "noncanonical envelope field",
			content: `{"SchemaVersion":1,"error":{"code":"write_outcome_unknown",` +
				`"details":{"reconciliation":` + string(validDescriptor) + `}}}`,
			message: "not a JSON error envelope",
		},
		{
			name: "unknown descriptor field",
			content: `{"schemaVersion":1,"error":{"code":"write_outcome_unknown",` +
				`"details":{"reconciliation":{"schemaVersion":1,"extra":true}}}}`,
			message: "descriptor is invalid",
		},
		{
			name: "duplicate descriptor field",
			content: `{"schemaVersion":1,"error":{"code":"write_outcome_unknown",` +
				`"details":{"reconciliation":{"schemaVersion":1,"schemaVersion":1}}}}`,
			message: "duplicate JSON field",
		},
		{
			name: "case-variant descriptor field",
			content: `{"schemaVersion":1,"error":{"code":"write_outcome_unknown",` +
				`"details":{"reconciliation":{"schemaVersion":1,"SchemaVersion":1}}}}`,
			message: "descriptor is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeAttemptFixture(t, test.content)
			result := runCLI(
				&recordingCreator{},
				&recordingReconciler{},
				[]string{"reconcile", "--attempt-file", path, "--json"},
				"",
			)
			if result.exitCode != 2 ||
				!strings.Contains(result.stderr, test.message) {
				t.Fatalf("run = %#v", result)
			}
		})
	}
}

func TestRunReconcileParserAndServiceFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reconciler Reconciler
		arguments  []string
		exitCode   int
		contains   string
	}{
		{
			name:       "missing file",
			reconciler: &recordingReconciler{},
			arguments:  []string{"reconcile", "--json"},
			exitCode:   2,
			contains:   "--attempt-file is required",
		},
		{
			name:       "unexpected positional",
			reconciler: &recordingReconciler{},
			arguments:  []string{"reconcile", "--attempt-file", "x", "--json", "42"},
			exitCode:   2,
			contains:   "accepts only flags",
		},
		{
			name:       "unknown flag prescan",
			reconciler: &recordingReconciler{},
			arguments:  []string{"reconcile", "--unknown", "--json"},
			exitCode:   2,
			contains:   `"code":"invalid_arguments"`,
		},
		{
			name:       "unavailable",
			reconciler: nil,
			arguments:  []string{"reconcile", "--attempt-file", "x", "--json"},
			exitCode:   7,
			contains:   "service is unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runCLI(
				&recordingCreator{},
				test.reconciler,
				test.arguments,
				"",
			)
			if result.exitCode != test.exitCode ||
				!strings.Contains(result.stderr, test.contains) {
				t.Fatalf("run = %#v", result)
			}
		})
	}
}

func TestRunReconcileMapsStableAndGenericFailures(t *testing.T) {
	t.Parallel()

	attemptPath := writeAttemptFixture(t, validAttemptEnvelope())
	for _, test := range []struct {
		name     string
		err      error
		exitCode int
		code     string
	}{
		{
			name: "stable",
			err: domain.NewFailure(
				domain.CodeRateLimited,
				"Limited.",
				nil,
				nil,
			),
			exitCode: 9,
			code:     "rate_limited",
		},
		{
			name:     "generic",
			err:      errors.New("failed"),
			exitCode: 7,
			code:     "github_api_error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runCLI(
				&recordingCreator{},
				&recordingReconciler{err: test.err},
				[]string{"reconcile", "--attempt-file", attemptPath, "--json"},
				"",
			)
			if result.exitCode != test.exitCode ||
				!strings.Contains(result.stderr, `"code":"`+test.code+`"`) {
				t.Fatalf("run = %#v", result)
			}
		})
	}
}

type recordingReconciler struct {
	requests []reconcile.Request
	result   reconcile.Result
	err      error
}

func (reconciler *recordingReconciler) Execute(
	_ context.Context,
	request reconcile.Request,
) (reconcile.Result, error) {
	reconciler.requests = append(reconciler.requests, request)
	return reconciler.result, reconciler.err
}

func writeAttemptFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attempt.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validAttemptEnvelope() string {
	descriptor := validDescriptor()
	envelope := map[string]any{
		"schemaVersion": 1,
		"error": map[string]any{
			"code":    domain.CodeWriteOutcomeUnknown,
			"message": "Unknown.",
			"details": map[string]any{
				"guidance":       "Do not retry.",
				"reconciliation": descriptor,
			},
		},
	}
	encoded, _ := json.Marshal(envelope)
	return string(encoded)
}

func validDescriptor() attempt.Descriptor {
	return attempt.Descriptor{
		SchemaVersion:    attempt.DescriptorSchemaVersion,
		Host:             "github.com",
		Repository:       "octo/example",
		PullRequest:      42,
		BaseSHA:          strings.Repeat("a", 40),
		HeadSHA:          strings.Repeat("b", 40),
		Event:            "COMMENT",
		ReviewBodySHA256: strings.Repeat("c", 64),
		Suggestions: []attempt.Suggestion{
			{
				Path:       "first.go",
				EndLine:    7,
				Side:       "RIGHT",
				BodySHA256: strings.Repeat("d", 64),
			},
		},
		RequestSHA256:    strings.Repeat("e", 64),
		AttemptStartedAt: time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC),
	}
}

func likelyReconciliationResult() reconcile.Result {
	return reconcile.Result{
		Status:           domain.ReconciliationLikelyCreated,
		Host:             "github.com",
		Repository:       "octo/example",
		PullRequest:      42,
		HeadSHA:          strings.Repeat("b", 40),
		RequestSHA256:    strings.Repeat("e", 64),
		AttemptStartedAt: time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC),
		SuggestionCount:  1,
		CandidateCount:   1,
		MatchCount:       1,
		ReviewID:         99,
		URL:              "https://github.com/octo/example/pull/42#pullrequestreview-99",
	}
}
