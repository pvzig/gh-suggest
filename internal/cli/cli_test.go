package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pvzig/gh-suggest/internal/create"
	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestRunCreateLoadsOneReviewWithOrderedRelativeReplacements(t *testing.T) {
	t.Parallel()

	reviewPath := writeReviewFixture(t, `{
		"schemaVersion": 1,
		"reviewBody": "Two focused fixes.",
		"suggestions": [
			{
				"path": "first.go",
				"endLine": 7,
				"replacementFile": "first.txt",
				"note": "First note."
			},
			{
				"path": "second.go",
				"startLine": 10,
				"endLine": 12,
				"replacementFile": "second.txt"
			}
		]
	}`, map[string]string{
		"first.txt":  "first replacement\n",
		"second.txt": "line one\nline two",
	})
	creator := &recordingCreator{result: validatedCreateResult()}
	result := runCLI(
		creator,
		nil,
		[]string{
			"create",
			"feature",
			"--repo",
			"octo/example",
			"--review-file",
			reviewPath,
			"--dry-run",
			"--json",
		},
		"",
	)

	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("run = %#v", result)
	}
	if !strings.Contains(result.stdout, `"schemaVersion":1`) ||
		!strings.Contains(result.stdout, `"status":"validated"`) ||
		!strings.Contains(result.stdout, `"suggestions":[`) {
		t.Fatalf("stdout = %q", result.stdout)
	}
	if len(creator.preflightRequests) != 1 || len(creator.executeRequests) != 1 {
		t.Fatalf(
			"preflight/execute calls = %d/%d, want 1/1",
			len(creator.preflightRequests),
			len(creator.executeRequests),
		)
	}
	request := creator.executeRequests[0]
	if request.Selector != "feature" ||
		request.Repository != "octo/example" ||
		request.ReviewBody != "Two focused fixes." ||
		!request.DryRun ||
		len(request.Suggestions) != 2 {
		t.Fatalf("request = %#v", request)
	}
	if string(request.Suggestions[0].Replacement) != "first replacement\n" ||
		string(request.Suggestions[1].Replacement) != "line one\nline two" ||
		request.Suggestions[0].Note != "First note." ||
		request.Suggestions[1].StartLine == nil ||
		*request.Suggestions[1].StartLine != 10 {
		t.Fatalf("suggestions = %#v", request.Suggestions)
	}
	for _, suggestion := range creator.preflightRequests[0].Suggestions {
		if suggestion.Replacement != nil {
			t.Fatalf("preflight opened replacements early: %#v", suggestion.Replacement)
		}
	}
}

func TestRunCreateMapsOptionalHeadGuardAndCreatedReview(t *testing.T) {
	t.Parallel()

	reviewPath := writeReviewFixture(t, validReviewJSON("replacement.txt"), map[string]string{
		"replacement.txt": "replacement",
	})
	creator := &recordingCreator{result: createdCreateResult()}
	head := strings.Repeat("c", 40)
	result := runCLI(
		creator,
		nil,
		[]string{
			"create",
			"42",
			"--review-file",
			reviewPath,
			"--head-sha",
			head,
			"--json",
		},
		"",
	)

	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("run = %#v", result)
	}
	if !strings.Contains(result.stdout, `"status":"created"`) ||
		!strings.Contains(result.stdout, `"reviewID":99`) {
		t.Fatalf("stdout = %q", result.stdout)
	}
	request := creator.executeRequests[0]
	if request.DryRun ||
		request.ExpectedHeadSHA != head {
		t.Fatalf("request = %#v", request)
	}
}

func TestRunCreatePreservesAmbiguousAttemptWithoutJSONFlag(t *testing.T) {
	t.Parallel()

	reviewPath := writeReviewFixture(t, validReviewJSON("replacement.txt"), map[string]string{
		"replacement.txt": "replacement",
	})
	ambiguous := domain.NewFailure(
		domain.CodeWriteOutcomeUnknown,
		"The suggestion review may have been created.",
		map[string]any{
			"reconciliation": validDescriptor(),
			"guidance":       "Do not retry the write.",
		},
		nil,
	)
	result := runCLI(
		&recordingCreator{executeError: ambiguous},
		nil,
		[]string{
			"create",
			"42",
			"--review-file",
			reviewPath,
		},
		"",
	)

	if result.exitCode != 10 ||
		result.stdout != "" ||
		!strings.HasPrefix(result.stderr, `{"schemaVersion":1,`) {
		t.Fatalf("run = %#v", result)
	}
	attemptPath := writeAttemptFixture(t, result.stderr)
	request, failure := parseAttemptFile(attemptPath, strings.NewReader(""))
	if failure != nil {
		t.Fatalf("parseAttemptFile() failure = %v", failure)
	}
	if request.Descriptor.RequestSHA256 != validDescriptor().RequestSHA256 {
		t.Fatalf("descriptor = %#v", request.Descriptor)
	}
}

func TestRunCreatePreflightFailurePrecedesReplacementIO(t *testing.T) {
	t.Parallel()

	reviewPath := writeReviewFixture(t, validReviewJSON("missing.txt"), nil)
	creator := &recordingCreator{
		preflightError: domain.NewFailure(
			domain.CodeUnsupportedHost,
			"Unsupported.",
			nil,
			nil,
		),
	}
	result := runCLI(
		creator,
		nil,
		[]string{
			"create",
			"42",
			"--review-file",
			reviewPath,
			"--dry-run",
			"--json",
		},
		"",
	)

	if result.exitCode != 11 ||
		!strings.Contains(result.stderr, `"code":"unsupported_host"`) {
		t.Fatalf("run = %#v", result)
	}
	if len(creator.executeRequests) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(creator.executeRequests))
	}
}

func TestRunCreateRejectsInvalidReviewFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		message string
	}{
		{
			name:    "unknown field",
			content: `{"schemaVersion":1,"reviewBody":"Body","suggestions":[],"extra":true}`,
			message: "not valid schema version 1 JSON",
		},
		{
			name:    "noncanonical top-level field",
			content: `{"SchemaVersion":1,"reviewBody":"Body","suggestions":[]}`,
			message: "not valid schema version 1 JSON",
		},
		{
			name:    "duplicate top-level field",
			content: `{"schemaVersion":1,"schemaVersion":1,"reviewBody":"Body","suggestions":[]}`,
			message: "duplicate JSON field",
		},
		{
			name: "duplicate nested field",
			content: `{"schemaVersion":1,"reviewBody":"Body","suggestions":[` +
				`{"path":"a","path":"b","endLine":1,"replacementFile":"x"}]}`,
			message: "duplicate JSON field",
		},
		{
			name: "case-variant nested field",
			content: `{"schemaVersion":1,"reviewBody":"Body","suggestions":[` +
				`{"path":"a","Path":"b","endLine":1,"replacementFile":"x"}]}`,
			message: "not valid schema version 1 JSON",
		},
		{
			name: "null start line",
			content: `{"schemaVersion":1,"reviewBody":"Body","suggestions":[` +
				`{"path":"a","startLine":null,"endLine":2,"replacementFile":"x"}]}`,
			message: "not valid schema version 1 JSON",
		},
		{
			name: "null note",
			content: `{"schemaVersion":1,"reviewBody":"Body","suggestions":[` +
				`{"path":"a","endLine":1,"replacementFile":"x","note":null}]}`,
			message: "not valid schema version 1 JSON",
		},
		{
			name:    "trailing value",
			content: `{"schemaVersion":1,"reviewBody":"Body","suggestions":[]} {}`,
			message: "exactly one JSON object",
		},
		{
			name:    "wrong schema",
			content: `{"schemaVersion":2,"reviewBody":"Body","suggestions":[]}`,
			message: "schemaVersion must be 1",
		},
		{
			name:    "no suggestions",
			content: `{"schemaVersion":1,"reviewBody":"Body","suggestions":[]}`,
			message: "at least one suggestion",
		},
		{
			name: "missing replacement file",
			content: `{"schemaVersion":1,"reviewBody":"Body","suggestions":[` +
				`{"path":"a","endLine":1}]}`,
			message: "requires replacementFile",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reviewPath := writeReviewFixture(t, test.content, nil)
			result := runCLI(
				&recordingCreator{},
				nil,
				[]string{
					"create",
					"42",
					"--review-file",
					reviewPath,
					"--dry-run",
					"--json",
				},
				"",
			)
			if result.exitCode != 2 ||
				!strings.Contains(result.stderr, test.message) {
				t.Fatalf("run = %#v, want message containing %q", result, test.message)
			}
		})
	}
}

func TestRunCreateAcceptsSchemaIntegerNumberForms(t *testing.T) {
	t.Parallel()

	reviewPath := writeReviewFixture(t, `{
		"schemaVersion": 1.0,
		"reviewBody": "One focused fix.",
		"suggestions": [{
			"path": "example.go",
			"startLine": 1e1,
			"endLine": 12.0,
			"replacementFile": "replacement.txt"
		}]
	}`, map[string]string{"replacement.txt": "replacement"})
	creator := &recordingCreator{result: validatedCreateResult()}
	result := runCLI(
		creator,
		nil,
		[]string{"create", "42", "--review-file", reviewPath, "--dry-run", "--json"},
		"",
	)
	if result.exitCode != 0 || result.stderr != "" {
		t.Fatalf("run = %#v", result)
	}
	request := creator.executeRequests[0]
	if request.Suggestions[0].StartLine == nil ||
		*request.Suggestions[0].StartLine != 10 ||
		request.Suggestions[0].EndLine != 12 {
		t.Fatalf("suggestion = %#v", request.Suggestions[0])
	}
}

func TestRunCreateRejectsInvalidUTF8AndOversizedInputs(t *testing.T) {
	t.Parallel()

	invalidPath := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidPath, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := runCLI(
		&recordingCreator{},
		nil,
		[]string{"create", "42", "--review-file", invalidPath, "--dry-run", "--json"},
		"",
	)
	if invalid.exitCode != 2 || !strings.Contains(invalid.stderr, "valid UTF-8") {
		t.Fatalf("invalid UTF-8 run = %#v", invalid)
	}

	largePath := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(largePath, []byte(strings.Repeat("x", maxReviewFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	large := runCLI(
		&recordingCreator{},
		nil,
		[]string{"create", "42", "--review-file", largePath, "--dry-run", "--json"},
		"",
	)
	if large.exitCode != 2 || !strings.Contains(large.stderr, "exceeds the 1 MiB limit") {
		t.Fatalf("large review run = %#v", large)
	}

	replacement := strings.Repeat("x", suggestionMaxBytes()+1)
	reviewPath := writeReviewFixture(t, validReviewJSON("large.txt"), map[string]string{
		"large.txt": replacement,
	})
	largeReplacement := runCLI(
		&recordingCreator{},
		nil,
		[]string{"create", "42", "--review-file", reviewPath, "--dry-run", "--json"},
		"",
	)
	if largeReplacement.exitCode != 2 ||
		!strings.Contains(largeReplacement.stderr, "per-file limit") {
		t.Fatalf("large replacement run = %#v", largeReplacement)
	}
}

func TestRunCreateStandardInputOwnership(t *testing.T) {
	t.Parallel()

	t.Run("one replacement may use stdin", func(t *testing.T) {
		reviewPath := writeReviewFixture(t, validReviewJSON("-"), nil)
		creator := &recordingCreator{result: validatedCreateResult()}
		result := runCLI(
			creator,
			nil,
			[]string{"create", "42", "--review-file", reviewPath, "--dry-run", "--json"},
			"from stdin",
		)
		if result.exitCode != 0 {
			t.Fatalf("run = %#v", result)
		}
		if got := string(creator.executeRequests[0].Suggestions[0].Replacement); got != "from stdin" {
			t.Fatalf("replacement = %q", got)
		}
	})

	t.Run("manifest and replacement cannot share stdin", func(t *testing.T) {
		result := runCLI(
			&recordingCreator{},
			nil,
			[]string{"create", "42", "--review-file", "-", "--dry-run", "--json"},
			validReviewJSON("-"),
		)
		if result.exitCode != 2 ||
			!strings.Contains(result.stderr, "Standard input can supply either") {
			t.Fatalf("run = %#v", result)
		}
	})

	t.Run("stdin aliases cannot be consumed twice", func(t *testing.T) {
		result := runCLI(
			&recordingCreator{},
			nil,
			[]string{"create", "42", "--review-file", "/dev/stdin", "--dry-run", "--json"},
			validReviewJSON("/dev/fd/0"),
		)
		if result.exitCode != 2 ||
			!strings.Contains(result.stderr, "Standard input can supply either") {
			t.Fatalf("run = %#v", result)
		}
	})
}

func TestRunCreateParserAndDispatchFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
		exitCode  int
		contains  string
	}{
		{
			name:      "missing review file",
			arguments: []string{"create", "--json"},
			exitCode:  2,
			contains:  "--review-file is required",
		},
		{
			name:      "trailing selector",
			arguments: []string{"create", "--review-file", "x", "--json", "42"},
			exitCode:  2,
			contains:  "Only one leading",
		},
		{
			name:      "trailing selector before JSON flag",
			arguments: []string{"create", "--review-file", "x", "42", "--json"},
			exitCode:  2,
			contains:  `"code":"invalid_arguments"`,
		},
		{
			name:      "unknown flag JSON prescan",
			arguments: []string{"create", "--unknown", "--json"},
			exitCode:  2,
			contains:  `"code":"invalid_arguments"`,
		},
		{
			name: "removed validated request flag",
			arguments: []string{
				"create",
				"--validated-request-sha256",
				strings.Repeat("a", 64),
				"--json",
			},
			exitCode: 2,
			contains: "flag provided but not defined",
		},
		{
			name:      "unknown command",
			arguments: []string{"other"},
			exitCode:  2,
			contains:  "Unknown command",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runCLI(&recordingCreator{}, nil, test.arguments, "")
			if result.exitCode != test.exitCode ||
				!strings.Contains(result.stderr, test.contains) {
				t.Fatalf("run = %#v", result)
			}
		})
	}
}

func TestRunHelpStaysOffline(t *testing.T) {
	t.Parallel()

	creator := &recordingCreator{}
	for _, arguments := range [][]string{{}, {"help"}, {"create", "--help"}} {
		result := runCLI(creator, nil, arguments, "")
		if result.exitCode != 0 || result.stderr != "" || !strings.Contains(result.stdout, "Usage:") {
			t.Fatalf("arguments %v run = %#v", arguments, result)
		}
	}
	if len(creator.executeRequests) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(creator.executeRequests))
	}
}

func TestRunCreateMapsStableAndGenericServiceFailures(t *testing.T) {
	t.Parallel()

	reviewPath := writeReviewFixture(t, validReviewJSON("replacement.txt"), map[string]string{
		"replacement.txt": "replacement",
	})
	for _, test := range []struct {
		name     string
		err      error
		exitCode int
		code     string
	}{
		{
			name: "stable",
			err: domain.NewFailure(
				domain.CodeStalePullRequest,
				"Stale.",
				nil,
				nil,
			),
			exitCode: 6,
			code:     "stale_pull_request",
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
				&recordingCreator{executeError: test.err},
				nil,
				[]string{"create", "42", "--review-file", reviewPath, "--dry-run", "--json"},
				"",
			)
			if result.exitCode != test.exitCode ||
				!strings.Contains(result.stderr, `"code":"`+test.code+`"`) {
				t.Fatalf("run = %#v", result)
			}
		})
	}
}

func TestRunCreateReportsDefinitiveWriteWhenOutputFails(t *testing.T) {
	t.Parallel()

	reviewPath := writeReviewFixture(t, validReviewJSON("replacement.txt"), map[string]string{
		"replacement.txt": "replacement",
	})
	creator := &recordingCreator{result: createdCreateResult()}
	var stderr strings.Builder
	exitCode := New(creator, nil).Run(
		context.Background(),
		[]string{"create", "42", "--review-file", reviewPath, "--json"},
		strings.NewReader(""),
		errorWriter{err: errors.New("stdout closed")},
		&stderr,
	)
	if exitCode != 8 ||
		!strings.Contains(stderr.String(), `"posted":true`) ||
		!strings.Contains(stderr.String(), `"reviewID":99`) ||
		!strings.Contains(stderr.String(), "do not retry") {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunCreateReportsDefinitiveWriteWhenSuccessConstructionFails(t *testing.T) {
	t.Parallel()

	reviewPath := writeReviewFixture(t, validReviewJSON("replacement.txt"), map[string]string{
		"replacement.txt": "replacement",
	})
	created := createdCreateResult()
	created.RequestSHA256 = "invalid"
	result := runCLI(
		&recordingCreator{result: created},
		nil,
		[]string{"create", "42", "--review-file", reviewPath, "--json"},
		"",
	)
	if result.exitCode != 8 ||
		!strings.Contains(result.stderr, `"posted":true`) ||
		!strings.Contains(result.stderr, `"reviewID":99`) ||
		!strings.Contains(result.stderr, created.URL) ||
		!strings.Contains(result.stderr, "do not retry") {
		t.Fatalf("run = %#v", result)
	}
}

type recordingCreator struct {
	preflightRequests []create.Request
	executeRequests   []create.Request
	preflightError    error
	executeError      error
	result            create.Result
}

func (creator *recordingCreator) Preflight(
	_ context.Context,
	request create.Request,
) error {
	creator.preflightRequests = append(
		creator.preflightRequests,
		cloneCreateRequest(request),
	)
	return creator.preflightError
}

func (creator *recordingCreator) Execute(
	_ context.Context,
	request create.Request,
) (create.Result, error) {
	creator.executeRequests = append(
		creator.executeRequests,
		cloneCreateRequest(request),
	)
	return creator.result, creator.executeError
}

func cloneCreateRequest(request create.Request) create.Request {
	cloned := request
	cloned.Suggestions = make([]create.Suggestion, len(request.Suggestions))
	for index, item := range request.Suggestions {
		cloned.Suggestions[index] = item
		cloned.Suggestions[index].Replacement = append([]byte(nil), item.Replacement...)
		if item.StartLine != nil {
			startLine := *item.StartLine
			cloned.Suggestions[index].StartLine = &startLine
		}
	}
	return cloned
}

type runResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func runCLI(
	creator Creator,
	reconciler Reconciler,
	arguments []string,
	stdin string,
) runResult {
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode := New(creator, reconciler).Run(
		context.Background(),
		arguments,
		strings.NewReader(stdin),
		&stdout,
		&stderr,
	)
	return runResult{
		exitCode: exitCode,
		stdout:   stdout.String(),
		stderr:   stderr.String(),
	}
}

func writeReviewFixture(
	t *testing.T,
	reviewJSON string,
	files map[string]string,
) string {
	t.Helper()
	directory := t.TempDir()
	for name, content := range files {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reviewPath := filepath.Join(directory, "review.json")
	if err := os.WriteFile(reviewPath, []byte(reviewJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return reviewPath
}

func validReviewJSON(replacementFile string) string {
	encoded, _ := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"reviewBody":    "Focused review.",
		"suggestions": []map[string]any{
			{
				"path":            "first.go",
				"endLine":         7,
				"replacementFile": replacementFile,
			},
		},
	})
	return string(encoded)
}

func validatedCreateResult() create.Result {
	hash := strings.Repeat("a", 64)
	return create.Result{
		Host:             "github.com",
		Repository:       "octo/example",
		PullRequest:      42,
		PullRequestURL:   "https://github.com/octo/example/pull/42",
		BaseSHA:          strings.Repeat("b", 40),
		HeadSHA:          strings.Repeat("c", 40),
		ReviewBodySHA256: hash,
		Suggestions: []create.SuggestionSummary{
			{
				Path:    "first.go",
				EndLine: 7,
				Replacement: create.ReplacementSummary{
					ByteCount: 11,
					LineCount: 1,
					SHA256:    hash,
				},
				BodySHA256: hash,
			},
		},
		RequestSHA256: hash,
	}
}

func createdCreateResult() create.Result {
	result := validatedCreateResult()
	result.Posted = true
	result.ReviewID = 99
	result.URL = "https://github.com/octo/example/pull/42#pullrequestreview-99"
	return result
}

func suggestionMaxBytes() int {
	return 1 << 20
}

type errorWriter struct {
	err error
}

func (writer errorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

var (
	_ io.Writer = errorWriter{}
)
