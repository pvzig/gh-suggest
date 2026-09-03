package schemas_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/pvzig/gh-suggest/internal/attempt"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/output"
)

const (
	reviewFileSchemaID = "https://raw.githubusercontent.com/pvzig/gh-suggest/main/schemas/review-file-v1.schema.json"
	outputSchemaID     = "https://raw.githubusercontent.com/pvzig/gh-suggest/main/schemas/output-v1.schema.json"
	descriptorSchemaID = "https://raw.githubusercontent.com/pvzig/gh-suggest/main/schemas/reconciliation-descriptor-v1.schema.json"
	metaSchemaID       = "https://json-schema.org/draft/2020-12/schema"
)

var schemaFiles = map[string]string{
	reviewFileSchemaID: "review-file-v1.schema.json",
	outputSchemaID:     "output-v1.schema.json",
	descriptorSchemaID: "reconciliation-descriptor-v1.schema.json",
}

type loadedSchema struct {
	compiled *jsonschema.Schema
	document map[string]any
}

func TestSchemasCompileAndExamplesConform(t *testing.T) {
	t.Parallel()

	for schemaID, loaded := range loadSchemas(t) {
		if loaded.document["$schema"] != metaSchemaID {
			t.Errorf("%s $schema = %v, want %s", schemaID, loaded.document["$schema"], metaSchemaID)
		}
		examples, ok := loaded.document["examples"].([]any)
		if !ok || len(examples) == 0 {
			t.Errorf("%s must document at least one example", schemaID)
			continue
		}
		for index, example := range examples {
			if err := loaded.compiled.Validate(example); err != nil {
				t.Errorf("%s example %d: %v", schemaID, index, err)
			}
		}
	}
}

func TestReviewFileSchemaRejectsInvalidStructure(t *testing.T) {
	t.Parallel()

	schema := loadSchemas(t)[reviewFileSchemaID].compiled
	tests := map[string]string{
		"wrong version":  `{"schemaVersion":2,"reviewBody":"Review.","suggestions":[{"path":"a.go","endLine":1,"replacementFile":"a.go"}]}`,
		"blank body":     `{"schemaVersion":1,"reviewBody":" ","suggestions":[{"path":"a.go","endLine":1,"replacementFile":"a.go"}]}`,
		"no suggestions": `{"schemaVersion":1,"reviewBody":"Review.","suggestions":[]}`,
		"unknown field":  `{"schemaVersion":1,"reviewBody":"Review.","suggestions":[{"path":"a.go","endLine":1,"replacementFile":"a.go","extra":true}]}`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if err := schema.Validate(decodeJSON(t, []byte(content))); err == nil {
				t.Fatal("Validate() succeeded, want schema violation")
			}
		})
	}
}

func TestEncodedContractsConformToSchemas(t *testing.T) {
	t.Parallel()

	schemas := loadSchemas(t)
	descriptor := validDescriptor()
	validateValue(t, schemas[descriptorSchemaID].compiled, descriptor)

	target := validTarget()
	validated, err := output.NewValidated(target)
	if err != nil {
		t.Fatalf("NewValidated() error = %v", err)
	}
	validateValue(t, schemas[outputSchemaID].compiled, validated)

	created, err := output.NewCreated(
		target,
		91,
		"https://github.com/octo/example/pull/42#pullrequestreview-91",
	)
	if err != nil {
		t.Fatalf("NewCreated() error = %v", err)
	}
	validateValue(t, schemas[outputSchemaID].compiled, created)

	reviewID := int64(91)
	reconciliation := output.Reconciliation{
		SchemaVersion:     output.SchemaVersion,
		Status:            domain.ReconciliationLikelyCreated,
		Host:              "github.com",
		Repository:        "octo/example",
		PullRequest:       42,
		HeadSHA:           strings.Repeat("b", 40),
		RequestSHA256:     strings.Repeat("f", 64),
		AttemptStartedAt:  "2026-08-08T12:00:00Z",
		SuggestionCount:   1,
		CandidateCount:    1,
		PartialMatchCount: 0,
		MatchCount:        1,
		ReviewID:          &reviewID,
		URL:               "https://github.com/octo/example/pull/42#pullrequestreview-91",
	}
	validateValue(t, schemas[outputSchemaID].compiled, reconciliation)

	unknownReconciliations := []output.Reconciliation{
		{
			SchemaVersion:    output.SchemaVersion,
			Status:           domain.ReconciliationUnknown,
			Host:             domain.SupportedHost,
			Repository:       "octo/example",
			PullRequest:      42,
			HeadSHA:          strings.Repeat("b", 40),
			RequestSHA256:    strings.Repeat("f", 64),
			AttemptStartedAt: "2026-08-08T12:00:00Z",
			SuggestionCount:  1,
		},
		{
			SchemaVersion:    output.SchemaVersion,
			Status:           domain.ReconciliationUnknown,
			Host:             domain.SupportedHost,
			Repository:       "octo/example",
			PullRequest:      42,
			HeadSHA:          strings.Repeat("b", 40),
			RequestSHA256:    strings.Repeat("f", 64),
			AttemptStartedAt: "2026-08-08T12:00:00Z",
			SuggestionCount:  1,
			CandidateCount:   2,
			MatchCount:       2,
		},
	}
	for _, unknown := range unknownReconciliations {
		if err := unknown.Validate(); err != nil {
			t.Fatalf("unknown reconciliation fixture is invalid: %v", err)
		}
		validateValue(t, schemas[outputSchemaID].compiled, unknown)
	}

	ordinaryFailure := output.NewErrorEnvelope(output.NewFailure(
		domain.CodeInvalidArguments,
		"The arguments are invalid.",
		map[string]any{"field": "reviewFile"},
	))
	validateValue(t, schemas[outputSchemaID].compiled, ordinaryFailure)

	for _, code := range []domain.Code{
		domain.CodeInvalidArguments,
		domain.CodeAuthenticationFailed,
		domain.CodeTargetNotFound,
		domain.CodeRangeNotCommentable,
		domain.CodeStalePullRequest,
		domain.CodeGitHubAPIError,
		domain.CodeIOError,
		domain.CodeRateLimited,
		domain.CodeUnsupportedHost,
		domain.CodeCancelled,
		domain.CodePermissionDenied,
	} {
		failure := output.NewErrorEnvelope(output.NewFailure(code, "Failure.", nil))
		validateValue(t, schemas[outputSchemaID].compiled, failure)
	}

	definitiveWriteOutputFailure := output.NewErrorEnvelope(output.NewFailure(
		domain.CodeIOError,
		"The review was created, but local output failed.",
		map[string]any{
			"posted":        true,
			"requestSHA256": strings.Repeat("f", 64),
			"reviewID":      int64(91),
			"url":           "https://github.com/octo/example/pull/42#pullrequestreview-91",
			"guidance":      "The review was created; do not retry the write.",
		},
	))
	validateValue(t, schemas[outputSchemaID].compiled, definitiveWriteOutputFailure)

	ambiguousFailure := output.NewErrorEnvelope(output.NewFailure(
		domain.CodeWriteOutcomeUnknown,
		"The review may have been created.",
		map[string]any{
			"reconciliation": descriptor,
			"guidance":       "Save this output and reconcile; do not retry.",
		},
	))
	validateValue(t, schemas[outputSchemaID].compiled, ambiguousFailure)
}

func TestOutputSchemaRejectsOldUnreleasedVersion(t *testing.T) {
	t.Parallel()

	validated, err := output.NewValidated(validTarget())
	if err != nil {
		t.Fatalf("NewValidated() error = %v", err)
	}
	encoded, err := json.Marshal(validated)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	instance := decodeJSON(t, encoded).(map[string]any)
	instance["schemaVersion"] = json.Number("2")
	if err := loadSchemas(t)[outputSchemaID].compiled.Validate(instance); err == nil {
		t.Fatal("Validate() succeeded for unreleased schemaVersion 2")
	}
}

func TestOutputSchemaAllowsAdditiveFields(t *testing.T) {
	t.Parallel()

	validated, err := output.NewValidated(validTarget())
	if err != nil {
		t.Fatalf("NewValidated() error = %v", err)
	}
	encoded, err := json.Marshal(validated)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	instance := decodeJSON(t, encoded).(map[string]any)
	instance["futureField"] = "permitted while schemaVersion remains 1"
	if err := loadSchemas(t)[outputSchemaID].compiled.Validate(instance); err != nil {
		t.Fatalf("Validate() additive field error = %v", err)
	}
}

func loadSchemas(t *testing.T) map[string]loadedSchema {
	t.Helper()

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	documents := make(map[string]map[string]any, len(schemaFiles))
	for schemaID, path := range schemaFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v", path, err)
		}
		document, ok := decodeJSON(t, content).(map[string]any)
		if !ok {
			t.Fatalf("%s is not a JSON object", path)
		}
		if document["$id"] != schemaID {
			t.Fatalf("%s $id = %v, want %s", path, document["$id"], schemaID)
		}
		if err := compiler.AddResource(schemaID, document); err != nil {
			t.Fatalf("AddResource(%q) error = %v", path, err)
		}
		documents[schemaID] = document
	}

	loaded := make(map[string]loadedSchema, len(schemaFiles))
	for schemaID := range schemaFiles {
		compiled, err := compiler.Compile(schemaID)
		if err != nil {
			t.Fatalf("Compile(%q) error = %v", schemaID, err)
		}
		loaded[schemaID] = loadedSchema{
			compiled: compiled,
			document: documents[schemaID],
		}
	}
	return loaded
}

func validateValue(t *testing.T, schema *jsonschema.Schema, value any) {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := schema.Validate(decodeJSON(t, encoded)); err != nil {
		t.Fatalf("Validate(%s) error = %v", encoded, err)
	}
}

func decodeJSON(t *testing.T, content []byte) any {
	t.Helper()

	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("UnmarshalJSON(%s) error = %v", content, err)
	}
	return value
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
				Path:       "Sources/Example.swift",
				EndLine:    42,
				Side:       "RIGHT",
				BodySHA256: strings.Repeat("d", 64),
			},
		},
		RequestSHA256:    strings.Repeat("e", 64),
		AttemptStartedAt: time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC),
	}
}

func validTarget() output.Target {
	return output.Target{
		Host:             "github.com",
		Repository:       "octo/example",
		PullRequest:      42,
		PullRequestURL:   "https://github.com/octo/example/pull/42",
		BaseSHA:          strings.Repeat("a", 40),
		HeadSHA:          strings.Repeat("b", 40),
		ReviewBodySHA256: strings.Repeat("c", 64),
		Suggestions: []output.Suggestion{
			{
				Path: "Sources/Example.swift",
				Range: output.Range{
					StartLine: 42,
					EndLine:   42,
					Side:      domain.SideRight,
				},
				Replacement: output.Replacement{
					ByteCount: 18,
					LineCount: 1,
					SHA256:    strings.Repeat("d", 64),
				},
				BodySHA256: strings.Repeat("e", 64),
			},
		},
		RequestSHA256: strings.Repeat("f", 64),
	}
}
