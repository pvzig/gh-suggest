package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/pvzig/gh-suggest/internal/create"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/jsoninput"
)

const (
	reviewFileSchemaVersion = 1
	maxReviewFileBytes      = 1 << 20
)

var errInvalidReviewFile = errors.New("review file is invalid")

type reviewFile struct {
	SchemaVersion schemaInteger          `json:"schemaVersion"`
	ReviewBody    string                 `json:"reviewBody"`
	Suggestions   []reviewFileSuggestion `json:"suggestions"`
}

type reviewFileSuggestion struct {
	Path            string         `json:"path"`
	StartLine       *schemaInteger `json:"startLine,omitempty"`
	EndLine         schemaInteger  `json:"endLine"`
	ReplacementFile string         `json:"replacementFile"`
	Note            string         `json:"note,omitempty"`
}

// schemaInteger accepts every JSON number that Draft 2020-12 considers an
// integer, including zero-fraction and exponent forms such as 1.0 and 1e0.
type schemaInteger int

func (integer *schemaInteger) UnmarshalJSON(encoded []byte) error {
	value, ok := new(big.Rat).SetString(string(encoded))
	if !ok || !value.IsInt() || !value.Num().IsInt64() {
		return errors.New("value must be a JSON integer")
	}
	parsed := value.Num().Int64()
	maximum := int64(^uint(0) >> 1)
	minimum := -maximum - 1
	if parsed < minimum || parsed > maximum {
		return errors.New("JSON integer is outside the platform int range")
	}
	*integer = schemaInteger(parsed)
	return nil
}

func parseReviewFile(
	path string,
	stdin io.Reader,
) (reviewFile, string, *domain.Failure) {
	content, baseDirectory, err := readReviewFile(path, stdin)
	if err != nil {
		code := domain.CodeIOError
		message := "The review file could not be read."
		if errors.Is(err, errInputTooLarge) {
			code = domain.CodeInvalidArguments
			message = "The review file exceeds the 1 MiB limit."
		}
		return reviewFile{}, "", domain.NewFailure(
			code,
			message,
			map[string]any{"reviewFile": path},
			err,
		)
	}
	if !utf8.Valid(content) {
		return reviewFile{}, "", invalidReviewFileFailure(
			"The review file must be valid UTF-8 JSON.",
			errInvalidReviewFile,
		)
	}
	if err := jsoninput.RejectDuplicateFields(content); errors.Is(err, jsoninput.ErrDuplicateField) {
		return reviewFile{}, "", invalidReviewFileFailure(
			"The review file contains a duplicate JSON field.",
			err,
		)
	}
	if err := validateReviewFileFieldNames(content); err != nil {
		return reviewFile{}, "", invalidReviewFileFailure(
			"The review file is not valid schema version 1 JSON.",
			err,
		)
	}

	var manifest reviewFile
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return reviewFile{}, "", invalidReviewFileFailure(
			"The review file is not valid schema version 1 JSON.",
			err,
		)
	}
	if err := jsoninput.RequireEOF(decoder); err != nil {
		return reviewFile{}, "", invalidReviewFileFailure(
			"The review file must contain exactly one JSON object.",
			err,
		)
	}
	if manifest.SchemaVersion != schemaInteger(reviewFileSchemaVersion) {
		return reviewFile{}, "", invalidReviewFileFailure(
			fmt.Sprintf(
				"The review file schemaVersion must be %d.",
				reviewFileSchemaVersion,
			),
			errInvalidReviewFile,
		)
	}
	if len(manifest.Suggestions) == 0 {
		return reviewFile{}, "", invalidReviewFileFailure(
			"The review file must contain at least one suggestion.",
			errInvalidReviewFile,
		)
	}
	for index, suggestion := range manifest.Suggestions {
		if suggestion.ReplacementFile == "" {
			return reviewFile{}, "", domain.NewFailure(
				domain.CodeInvalidArguments,
				"Every review suggestion requires replacementFile.",
				map[string]any{"suggestionIndex": index},
				errInvalidReviewFile,
			)
		}
	}

	return manifest, baseDirectory, nil
}

func requestFromReviewFile(
	manifest reviewFile,
	selector string,
	repository string,
	dryRun bool,
	expectedHeadSHA string,
) create.Request {
	suggestions := make([]create.Suggestion, len(manifest.Suggestions))
	for index, input := range manifest.Suggestions {
		var startLine *int
		if input.StartLine != nil {
			value := int(*input.StartLine)
			startLine = &value
		}
		suggestions[index] = create.Suggestion{
			Path:      input.Path,
			StartLine: startLine,
			EndLine:   int(input.EndLine),
			Note:      input.Note,
		}
	}
	return create.Request{
		Selector:        selector,
		Repository:      repository,
		ReviewBody:      manifest.ReviewBody,
		Suggestions:     suggestions,
		DryRun:          dryRun,
		ExpectedHeadSHA: expectedHeadSHA,
	}
}

func readReviewReplacements(
	request *create.Request,
	manifest reviewFile,
	baseDirectory string,
	reviewFilePath string,
	stdin io.Reader,
) *domain.Failure {
	stdinCount := 0
	for _, suggestion := range manifest.Suggestions {
		if isStandardInputPath(suggestion.ReplacementFile) {
			stdinCount++
		}
	}
	if stdinCount > 1 || (stdinCount == 1 && isStandardInputPath(reviewFilePath)) {
		return domain.NewFailure(
			domain.CodeInvalidArguments,
			"Standard input can supply either the review file or one replacement file, not both or several replacements.",
			nil,
			nil,
		)
	}

	for index, input := range manifest.Suggestions {
		replacementPath := input.ReplacementFile
		if !isStandardInputPath(replacementPath) && !filepath.IsAbs(replacementPath) {
			replacementPath = filepath.Join(baseDirectory, replacementPath)
		}
		replacement, err := readReplacement(replacementPath, stdin)
		if err != nil {
			code := domain.CodeIOError
			message := "A review replacement file could not be read."
			details := map[string]any{
				"suggestionIndex": index,
				"replacementFile": input.ReplacementFile,
			}
			if errors.Is(err, errReplacementTooLarge) {
				code = domain.CodeInvalidArguments
				message = "A review replacement exceeds the 1 MiB per-file limit."
				details["maximumBytes"] = 1 << 20
			}
			return domain.NewFailure(code, message, details, err)
		}
		request.Suggestions[index].Replacement = replacement
	}
	return nil
}

func readReviewFile(path string, stdin io.Reader) ([]byte, string, error) {
	content, err := readBoundedInput(path, stdin, maxReviewFileBytes)
	if err != nil {
		return nil, "", err
	}

	var baseDirectory string
	if isStandardInputPath(path) {
		currentDirectory, err := os.Getwd()
		if err != nil {
			return nil, "", err
		}
		baseDirectory = currentDirectory
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, "", err
		}
		baseDirectory = filepath.Dir(absolute)
	}
	return content, baseDirectory, nil
}

func validateReviewFileFieldNames(content []byte) error {
	root, err := jsoninput.ValidateObjectFields(
		content,
		[]string{"schemaVersion", "reviewBody", "suggestions"},
		false,
	)
	if err != nil {
		return err
	}

	rawSuggestions, ok := root["suggestions"]
	if !ok {
		return nil
	}
	var suggestions []json.RawMessage
	if err := json.Unmarshal(rawSuggestions, &suggestions); err != nil {
		return err
	}
	for _, rawSuggestion := range suggestions {
		fields, err := jsoninput.ValidateObjectFields(
			rawSuggestion,
			[]string{"path", "startLine", "endLine", "replacementFile", "note"},
			false,
		)
		if err != nil {
			return err
		}
		for _, name := range []string{"startLine", "note"} {
			if value, present := fields[name]; present &&
				bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("%s must not be null", name)
			}
		}
	}
	return nil
}

func invalidReviewFileFailure(message string, cause error) *domain.Failure {
	return domain.NewFailure(
		domain.CodeInvalidArguments,
		message,
		map[string]any{"field": "reviewFile"},
		cause,
	)
}
