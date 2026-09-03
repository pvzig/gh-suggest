// Package diff parses pull-request diffs and proves whether right-side ranges
// are visible and commentable.
package diff

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

// MaxDiffBytes is the largest raw pull-request diff that can be treated as
// complete. A response at or above this boundary may have been truncated by
// GitHub and must be rejected.
const MaxDiffBytes int64 = 20 * 1024 * 1024

// Reason is a stable, machine-readable explanation for a validation failure.
type Reason string

// The reasons a range cannot be proven commentable. Each value is part of the
// stable error-details contract.
const (
	ReasonDiffUnavailable      Reason = "diff_unavailable"
	ReasonDiffTooLarge         Reason = "diff_too_large"
	ReasonDiffIncomplete       Reason = "diff_incomplete"
	ReasonTextPatchUnavailable Reason = "text_patch_unavailable"
	ReasonPathNotFound         Reason = "path_not_found"
	ReasonInvalidRange         Reason = "invalid_range"
	ReasonRangeNotVisible      Reason = "range_not_visible"
	ReasonRangeCrossesHunks    Reason = "range_crosses_hunks"
)

// FileKind describes how a file changed.
type FileKind string

// The ways a file can change in a diff.
const (
	FileModified FileKind = "modified"
	FileRenamed  FileKind = "renamed"
	FileCopied   FileKind = "copied"
	FileAdded    FileKind = "added"
	FileDeleted  FileKind = "deleted"
)

// Details contains non-sensitive, machine-readable context for a validation
// failure. Reason is always populated.
type Details struct {
	Reason Reason `json:"reason"`

	Path     string   `json:"path,omitempty"`
	OldPath  string   `json:"oldPath,omitempty"`
	NewPath  string   `json:"newPath,omitempty"`
	FileKind FileKind `json:"fileKind,omitempty"`
	Binary   bool     `json:"binary,omitempty"`

	StartLine int64 `json:"startLine,omitempty"`
	EndLine   int64 `json:"endLine,omitempty"`

	StartHunk int `json:"startHunk,omitempty"`
	EndHunk   int `json:"endHunk,omitempty"`

	ExpectedFileCount int `json:"expectedFileCount,omitempty"`
	ParsedFileCount   int `json:"parsedFileCount,omitempty"`

	Size  int64 `json:"size,omitempty"`
	Limit int64 `json:"limit,omitempty"`

	Cause string `json:"cause,omitempty"`
}

// ValidationError is returned whenever the package cannot prove that a
// requested range is valid. Callers can use errors.As and inspect Details.
type ValidationError struct {
	message string
	cause   error

	Details Details
}

func (validationError *ValidationError) Error() string {
	return validationError.message
}

func (validationError *ValidationError) Unwrap() error {
	return validationError.cause
}

func tooLargeError(size int64) *ValidationError {
	return newValidationError(
		ReasonDiffTooLarge,
		"The pull request diff is too large to verify safely.",
		Details{Size: size, Limit: MaxDiffBytes},
		nil,
	)
}

// Diff is a validated index of right-side ranges visible in a pull-request diff.
type Diff struct {
	files []file
}

type file struct {
	oldPath string
	newPath string
	kind    FileKind
	binary  bool
	hunks   []hunk
}

type hunk struct {
	start     int64
	lineCount int64
}

// Parse parses a complete Git or unified diff and verifies that it contains
// exactly expectedFileCount file records. It rejects any input for which
// syntactic completeness cannot be proven.
func Parse(raw []byte, expectedFileCount int) (Diff, error) {
	if expectedFileCount < 0 {
		return Diff{}, incompleteError(
			"negative_expected_file_count",
			expectedFileCount,
			0,
			nil,
		)
	}

	size := int64(len(raw))
	if size >= MaxDiffBytes {
		return Diff{}, tooLargeError(size)
	}

	if len(raw) == 0 {
		if expectedFileCount == 0 {
			return Diff{}, nil
		}
		return Diff{}, incompleteError(
			"empty_diff_for_changed_files",
			expectedFileCount,
			0,
			nil,
		)
	}

	// GitHub's Git-formatted diff records are line-oriented. Requiring the
	// terminal LF catches a response cut off between parser reads while still
	// allowing source files whose final line has no LF (Git represents that
	// state with its own complete marker line).
	if raw[len(raw)-1] != '\n' {
		return Diff{}, incompleteError(
			"missing_terminal_newline",
			expectedFileCount,
			0,
			nil,
		)
	}

	parsedFiles, preamble, err := gitdiff.Parse(bytes.NewReader(raw))
	if err != nil {
		return Diff{}, incompleteError(
			"parser_error",
			expectedFileCount,
			len(parsedFiles),
			err,
		)
	}

	if strings.TrimSpace(preamble) != "" {
		return Diff{}, incompleteError(
			"unexpected_preamble",
			expectedFileCount,
			len(parsedFiles),
			nil,
		)
	}

	// A GitHub raw diff uses one "diff --git" header per changed file. This
	// extra check catches parser recovery that might otherwise skip a malformed
	// file header. Traditional unified diffs have no such headers, so their
	// exactness remains guarded by expectedFileCount.
	if headerCount := countGitFileHeaders(raw); headerCount > 0 && headerCount != len(parsedFiles) {
		return Diff{}, incompleteError(
			"git_header_count_mismatch",
			headerCount,
			len(parsedFiles),
			nil,
		)
	}

	if len(parsedFiles) != expectedFileCount {
		return Diff{}, incompleteError(
			"file_count_mismatch",
			expectedFileCount,
			len(parsedFiles),
			nil,
		)
	}

	result := Diff{files: make([]file, 0, len(parsedFiles))}
	rightPaths := make(map[string]struct{}, len(parsedFiles))
	for index, parsedFile := range parsedFiles {
		file, convertErr := convertFile(parsedFile)
		if convertErr != nil {
			return Diff{}, incompleteError(
				fmt.Sprintf("invalid_file_%d", index+1),
				expectedFileCount,
				len(parsedFiles),
				convertErr,
			)
		}
		if file.newPath != "" {
			if _, exists := rightPaths[file.newPath]; exists {
				return Diff{}, incompleteError(
					"duplicate_right_path",
					expectedFileCount,
					len(parsedFiles),
					nil,
				)
			}
			rightPaths[file.newPath] = struct{}{}
		}
		result.files = append(result.files, file)
	}

	return result, nil
}

// ValidateRightRange proves that every line from startLine through endLine is
// visible on the right side of one textual hunk in path.
func (parsedDiff Diff) ValidateRightRange(path string, startLine, endLine int64) error {
	if path == "" || startLine <= 0 || endLine < startLine {
		return newValidationError(
			ReasonInvalidRange,
			"The requested right-side path and line range are invalid.",
			Details{
				Path:      path,
				StartLine: startLine,
				EndLine:   endLine,
			},
			nil,
		)
	}

	for index := range parsedDiff.files {
		file := &parsedDiff.files[index]
		if file.newPath == path {
			return file.validateRightRange(startLine, endLine)
		}
	}

	// A deleted file has no right-side path, but reporting its parsed state is
	// more useful than pretending that the old path was never observed.
	for index := range parsedDiff.files {
		file := &parsedDiff.files[index]
		if file.oldPath != path {
			continue
		}

		if file.kind == FileDeleted {
			return file.textPatchUnavailableError(path, startLine, endLine)
		}

		if file.kind == FileRenamed || file.kind == FileCopied {
			return newValidationError(
				ReasonPathNotFound,
				"The requested path is not the file's verified right-side path.",
				Details{
					Path:      path,
					OldPath:   file.oldPath,
					NewPath:   file.newPath,
					FileKind:  file.kind,
					StartLine: startLine,
					EndLine:   endLine,
				},
				nil,
			)
		}
	}

	return newValidationError(
		ReasonPathNotFound,
		"The requested path is not present on the verified right side of the diff.",
		Details{
			Path:      path,
			StartLine: startLine,
			EndLine:   endLine,
		},
		nil,
	)
}

func (file *file) validateRightRange(startLine, endLine int64) error {
	if file.kind == FileDeleted || file.binary || len(file.hunks) == 0 {
		return file.textPatchUnavailableError(file.newPath, startLine, endLine)
	}

	startHunk := 0
	endHunk := 0
	for index, hunk := range file.hunks {
		hunkNumber := index + 1
		if hunk.containsRightLine(startLine) {
			startHunk = hunkNumber
		}
		if hunk.containsRightLine(endLine) {
			endHunk = hunkNumber
		}
		if hunk.containsRightRange(startLine, endLine) {
			return nil
		}
	}

	if startHunk != 0 && endHunk != 0 && startHunk != endHunk {
		return newValidationError(
			ReasonRangeCrossesHunks,
			"The requested right-side range crosses diff hunks.",
			Details{
				Path:      file.newPath,
				OldPath:   file.oldPath,
				NewPath:   file.newPath,
				FileKind:  file.kind,
				StartLine: startLine,
				EndLine:   endLine,
				StartHunk: startHunk,
				EndHunk:   endHunk,
			},
			nil,
		)
	}

	return newValidationError(
		ReasonRangeNotVisible,
		"The requested right-side range is not fully visible in one diff hunk.",
		Details{
			Path:      file.newPath,
			OldPath:   file.oldPath,
			NewPath:   file.newPath,
			FileKind:  file.kind,
			StartLine: startLine,
			EndLine:   endLine,
			StartHunk: startHunk,
			EndHunk:   endHunk,
		},
		nil,
	)
}

func (file *file) textPatchUnavailableError(path string, startLine, endLine int64) *ValidationError {
	return newValidationError(
		ReasonTextPatchUnavailable,
		"The requested file does not have a usable right-side text patch.",
		Details{
			Path:      path,
			OldPath:   file.oldPath,
			NewPath:   file.newPath,
			FileKind:  file.kind,
			Binary:    file.binary,
			StartLine: startLine,
			EndLine:   endLine,
		},
		nil,
	)
}

func (hunk hunk) containsRightLine(line int64) bool {
	if hunk.start <= 0 || hunk.lineCount <= 0 || line < hunk.start {
		return false
	}
	return line-hunk.start < hunk.lineCount
}

func (hunk hunk) containsRightRange(startLine, endLine int64) bool {
	return hunk.containsRightLine(startLine) && hunk.containsRightLine(endLine)
}

func convertFile(parsed *gitdiff.File) (file, error) {
	if parsed == nil {
		return file{}, errors.New("nil file record")
	}

	flagCount := 0
	for _, set := range []bool{parsed.IsNew, parsed.IsDelete, parsed.IsCopy, parsed.IsRename} {
		if set {
			flagCount++
		}
	}
	if flagCount > 1 {
		return file{}, errors.New("file record has conflicting change kinds")
	}
	if parsed.IsBinary && len(parsed.TextFragments) > 0 {
		return file{}, errors.New("file record contains binary and text fragments")
	}

	converted := file{
		oldPath: parsed.OldName,
		newPath: parsed.NewName,
		kind:    fileKind(parsed),
		binary:  parsed.IsBinary,
		hunks:   make([]hunk, 0, len(parsed.TextFragments)),
	}

	switch converted.kind {
	case FileAdded:
		if converted.oldPath != "" || converted.newPath == "" {
			return file{}, errors.New("added file has inconsistent paths")
		}
	case FileDeleted:
		if converted.oldPath == "" || converted.newPath != "" {
			return file{}, errors.New("deleted file has inconsistent paths")
		}
	default:
		if converted.oldPath == "" || converted.newPath == "" {
			return file{}, errors.New("file record is missing a path")
		}
	}

	var previousOldStart int64 = -1
	var previousNewStart int64 = -1
	var previousOldEnd int64
	var previousNewEnd int64
	var hasOldRange bool
	var hasNewRange bool

	for index, fragment := range parsed.TextFragments {
		convertedHunk, oldStart, err := convertHunk(fragment)
		if err != nil {
			return file{}, fmt.Errorf("hunk %d: %w", index+1, err)
		}
		if oldStart < previousOldStart || convertedHunk.start < previousNewStart {
			return file{}, fmt.Errorf("hunk %d is out of order", index+1)
		}
		if fragment.OldLines > 0 {
			if hasOldRange && oldStart <= previousOldEnd {
				return file{}, fmt.Errorf("hunk %d: overlaps an earlier old-side line", index+1)
			}
			previousOldEnd = oldStart + fragment.OldLines - 1
			hasOldRange = true
		}
		if convertedHunk.lineCount > 0 {
			if hasNewRange && convertedHunk.start <= previousNewEnd {
				return file{}, fmt.Errorf("hunk %d: overlaps an earlier right-side line", index+1)
			}
			previousNewEnd = convertedHunk.start + convertedHunk.lineCount - 1
			hasNewRange = true
		}
		previousOldStart = oldStart
		previousNewStart = convertedHunk.start
		converted.hunks = append(converted.hunks, convertedHunk)
	}

	return converted, nil
}

func convertHunk(fragment *gitdiff.TextFragment) (hunk, int64, error) {
	if fragment == nil {
		return hunk{}, 0, errors.New("nil text fragment")
	}
	if err := fragment.Validate(); err != nil {
		return hunk{}, 0, fmt.Errorf("invalid text fragment: %w", err)
	}
	if err := validateFragmentRange("old", fragment.OldPosition, fragment.OldLines); err != nil {
		return hunk{}, 0, err
	}
	if err := validateFragmentRange("new", fragment.NewPosition, fragment.NewLines); err != nil {
		return hunk{}, 0, err
	}

	converted := hunk{
		start:     fragment.NewPosition,
		lineCount: fragment.NewLines,
	}

	return converted, fragment.OldPosition, nil
}

func validateFragmentRange(side string, position, count int64) error {
	if position < 0 || count < 0 {
		return fmt.Errorf("%s-side hunk range is negative", side)
	}
	if count > 0 && position == 0 {
		return fmt.Errorf("%s-side non-empty hunk starts at zero", side)
	}
	if count > 0 && position > (int64(^uint64(0)>>1)-(count-1)) {
		return fmt.Errorf("%s-side hunk range overflows", side)
	}
	return nil
}

func fileKind(file *gitdiff.File) FileKind {
	switch {
	case file.IsNew:
		return FileAdded
	case file.IsDelete:
		return FileDeleted
	case file.IsRename:
		return FileRenamed
	case file.IsCopy:
		return FileCopied
	default:
		return FileModified
	}
}

func countGitFileHeaders(raw []byte) int {
	count := 0
	for len(raw) > 0 {
		end := bytes.IndexByte(raw, '\n')
		if end < 0 {
			end = len(raw)
		}
		if bytes.HasPrefix(raw[:end], []byte("diff --git ")) {
			count++
		}
		if end == len(raw) {
			break
		}
		raw = raw[end+1:]
	}
	return count
}

func incompleteError(cause string, expectedFileCount, parsedFileCount int, err error) *ValidationError {
	details := Details{
		ExpectedFileCount: expectedFileCount,
		ParsedFileCount:   parsedFileCount,
		Cause:             cause,
	}
	return newValidationError(
		ReasonDiffIncomplete,
		"The pull request diff is incomplete, so the range cannot be verified.",
		details,
		err,
	)
}

func newValidationError(
	reason Reason,
	message string,
	details Details,
	cause error,
) *ValidationError {
	details.Reason = reason
	if cause != nil && details.Cause == "" {
		details.Cause = cause.Error()
	}
	return &ValidationError{
		message: message,
		cause:   cause,
		Details: details,
	}
}
