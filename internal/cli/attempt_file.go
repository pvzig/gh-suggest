package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/pvzig/gh-suggest/internal/attempt"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/jsoninput"
	"github.com/pvzig/gh-suggest/internal/output"
	"github.com/pvzig/gh-suggest/internal/reconcile"
)

const maxAttemptFileBytes = 1 << 20

var errInvalidAttemptFile = errors.New("ambiguous-attempt file is invalid")

func parseAttemptFile(path string, stdin io.Reader) (reconcile.Request, *domain.Failure) {
	content, err := readAttemptFile(path, stdin)
	if err != nil {
		code := domain.CodeIOError
		message := "The ambiguous-attempt file could not be read."
		if errors.Is(err, errInputTooLarge) {
			code = domain.CodeInvalidArguments
			message = "The ambiguous-attempt file exceeds the 1 MiB limit."
		}
		return reconcile.Request{}, domain.NewFailure(
			code,
			message,
			map[string]any{"attemptFile": path},
			err,
		)
	}
	if !utf8.Valid(content) {
		return reconcile.Request{}, invalidAttemptFileFailure(
			"The ambiguous-attempt file must be valid UTF-8 JSON.",
			errInvalidAttemptFile,
		)
	}
	if err := jsoninput.RejectDuplicateFields(content); errors.Is(err, jsoninput.ErrDuplicateField) {
		return reconcile.Request{}, invalidAttemptFileFailure(
			"The ambiguous-attempt file contains a duplicate JSON field.",
			err,
		)
	}
	if err := validateAttemptEnvelopeFieldNames(content); err != nil {
		return reconcile.Request{}, invalidAttemptFileFailure(
			"The ambiguous-attempt file is not a JSON error envelope.",
			err,
		)
	}

	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
		Error         struct {
			Code    domain.Code `json:"code"`
			Details struct {
				Reconciliation json.RawMessage `json:"reconciliation"`
			} `json:"details"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&envelope); err != nil {
		return reconcile.Request{}, invalidAttemptFileFailure(
			"The ambiguous-attempt file is not a JSON error envelope.",
			err,
		)
	}
	if err := jsoninput.RequireEOF(decoder); err != nil {
		return reconcile.Request{}, invalidAttemptFileFailure(
			"The ambiguous-attempt file must contain exactly one JSON object.",
			err,
		)
	}
	if envelope.SchemaVersion != output.SchemaVersion {
		return reconcile.Request{}, invalidAttemptFileFailure(
			fmt.Sprintf(
				"The ambiguous-attempt file schemaVersion must be %d.",
				output.SchemaVersion,
			),
			errInvalidAttemptFile,
		)
	}
	if envelope.Error.Code != domain.CodeWriteOutcomeUnknown ||
		len(envelope.Error.Details.Reconciliation) == 0 {
		return reconcile.Request{}, invalidAttemptFileFailure(
			"The ambiguous-attempt file must contain write_outcome_unknown reconciliation details.",
			errInvalidAttemptFile,
		)
	}
	descriptor, err := attempt.DecodeDescriptor(envelope.Error.Details.Reconciliation)
	if err != nil {
		return reconcile.Request{}, invalidAttemptFileFailure(
			"The ambiguous-attempt reconciliation descriptor is invalid.",
			err,
		)
	}
	return reconcile.Request{Descriptor: descriptor}, nil
}

func validateAttemptEnvelopeFieldNames(content []byte) error {
	envelope, err := jsoninput.ValidateObjectFields(
		content,
		[]string{"schemaVersion", "error"},
		true,
	)
	if err != nil {
		return err
	}
	rawError, ok := envelope["error"]
	if !ok {
		return nil
	}
	errorObject, err := jsoninput.ValidateObjectFields(
		rawError,
		[]string{"code", "message", "details"},
		true,
	)
	if err != nil {
		return err
	}
	rawDetails, ok := errorObject["details"]
	if !ok {
		return nil
	}
	_, err = jsoninput.ValidateObjectFields(
		rawDetails,
		[]string{"reconciliation", "guidance"},
		true,
	)
	return err
}

func readAttemptFile(path string, stdin io.Reader) ([]byte, error) {
	return readBoundedInput(path, stdin, maxAttemptFileBytes)
}

func invalidAttemptFileFailure(message string, cause error) *domain.Failure {
	return domain.NewFailure(
		domain.CodeInvalidArguments,
		message,
		map[string]any{"field": "attemptFile"},
		cause,
	)
}
