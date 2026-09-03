package diff

import (
	"errors"
	"strings"
	"testing"
)

const multiHunkDiff = `diff --git a/example.txt b/example.txt
index 1111111..2222222 100644
--- a/example.txt
+++ b/example.txt
@@ -10,4 +10,4 @@
 context ten
-old eleven
+new eleven
 context twelve
 context thirteen
@@ -30,2 +30,3 @@
 context thirty
+new thirty-one
 context thirty-two
`

const renamedDiff = `diff --git a/old.txt b/dir/new.txt
similarity index 50%
rename from old.txt
rename to dir/new.txt
index 1111111..2222222 100644
--- a/old.txt
+++ b/dir/new.txt
@@ -1 +1 @@
-old
+new
`

const newFileDiff = `diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..2222222
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+one
+two
`

const deletedFileDiff = `diff --git a/deleted.txt b/deleted.txt
deleted file mode 100644
index 1111111..0000000
--- a/deleted.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-one
-two
`

const binaryDiff = `diff --git a/image.png b/image.png
index 1111111..2222222 100644
Binary files a/image.png and b/image.png differ
`

const modeOnlyDiff = `diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755
`

func TestValidateRightRangeAcceptsContextAddedAndMultiLineRanges(t *testing.T) {
	parsed := mustParse(t, multiHunkDiff, 1)

	for _, test := range []struct {
		name  string
		start int64
		end   int64
	}{
		{name: "context line", start: 10, end: 10},
		{name: "added line", start: 11, end: 11},
		{name: "first whole hunk", start: 10, end: 13},
		{name: "second whole hunk", start: 30, end: 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := parsed.ValidateRightRange("example.txt", test.start, test.end); err != nil {
				t.Fatalf("ValidateRightRange() error = %v", err)
			}
		})
	}
}

func TestValidateRightRangeRejectsRangesAcrossHunks(t *testing.T) {
	parsed := mustParse(t, multiHunkDiff, 1)

	err := parsed.ValidateRightRange("example.txt", 13, 30)
	validationError := requireReason(t, err, ReasonRangeCrossesHunks)
	if validationError.Details.StartHunk != 1 || validationError.Details.EndHunk != 2 {
		t.Fatalf(
			"hunk details = %d -> %d, want 1 -> 2",
			validationError.Details.StartHunk,
			validationError.Details.EndHunk,
		)
	}
}

func TestValidateRightRangeRejectsPartiallyOrFullyInvisibleRanges(t *testing.T) {
	parsed := mustParse(t, multiHunkDiff, 1)

	for _, test := range []struct {
		name      string
		start     int64
		end       int64
		startHunk int
		endHunk   int
	}{
		{name: "both outside hunks", start: 20, end: 21},
		{name: "end outside hunk", start: 13, end: 14, startHunk: 1},
		{name: "start outside hunk", start: 29, end: 30, endHunk: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := parsed.ValidateRightRange("example.txt", test.start, test.end)
			validationError := requireReason(t, err, ReasonRangeNotVisible)
			if validationError.Details.StartHunk != test.startHunk ||
				validationError.Details.EndHunk != test.endHunk {
				t.Fatalf(
					"hunk details = %d -> %d, want %d -> %d",
					validationError.Details.StartHunk,
					validationError.Details.EndHunk,
					test.startHunk,
					test.endHunk,
				)
			}
		})
	}
}

func TestValidateRightRangeRejectsInvalidInputs(t *testing.T) {
	parsed := mustParse(t, multiHunkDiff, 1)

	for _, test := range []struct {
		name  string
		path  string
		start int64
		end   int64
	}{
		{name: "empty path", start: 1, end: 1},
		{name: "zero start", path: "example.txt", end: 1},
		{name: "negative start", path: "example.txt", start: -1, end: 1},
		{name: "reversed range", path: "example.txt", start: 2, end: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := parsed.ValidateRightRange(test.path, test.start, test.end)
			_ = requireReason(t, err, ReasonInvalidRange)
		})
	}
}

func TestParseAndValidateRenamedFileUsesNewPath(t *testing.T) {
	parsed := mustParse(t, renamedDiff, 1)

	if err := parsed.ValidateRightRange("dir/new.txt", 1, 1); err != nil {
		t.Fatalf("new-path validation error = %v", err)
	}

	err := parsed.ValidateRightRange("old.txt", 1, 1)
	validationError := requireReason(t, err, ReasonPathNotFound)
	if validationError.Details.NewPath != "dir/new.txt" {
		t.Fatalf("Details.NewPath = %q, want dir/new.txt", validationError.Details.NewPath)
	}
}

func TestParseAndValidateNewFile(t *testing.T) {
	parsed := mustParse(t, newFileDiff, 1)
	if err := parsed.ValidateRightRange("new.txt", 1, 2); err != nil {
		t.Fatalf("ValidateRightRange() error = %v", err)
	}
}

func TestParseTraditionalUnifiedDiff(t *testing.T) {
	raw := `--- example.txt
+++ example.txt
@@ -1 +1 @@
-old
+new
`
	parsed := mustParse(t, raw, 1)
	if err := parsed.ValidateRightRange("example.txt", 1, 1); err != nil {
		t.Fatalf("ValidateRightRange() error = %v", err)
	}
}

func TestDeletedFileHasNoUsableRightSide(t *testing.T) {
	parsed := mustParse(t, deletedFileDiff, 1)
	err := parsed.ValidateRightRange("deleted.txt", 1, 1)
	validationError := requireReason(t, err, ReasonTextPatchUnavailable)
	if validationError.Details.FileKind != FileDeleted {
		t.Fatalf("Details.FileKind = %q, want %q", validationError.Details.FileKind, FileDeleted)
	}
}

func TestBinaryFileReportsTextPatchUnavailable(t *testing.T) {
	parsed := mustParse(t, binaryDiff, 1)
	err := parsed.ValidateRightRange("image.png", 1, 1)
	validationError := requireReason(t, err, ReasonTextPatchUnavailable)
	if !validationError.Details.Binary {
		t.Fatal("Details.Binary = false, want true")
	}
}

func TestFileWithoutTextFragmentsReportsTextPatchUnavailable(t *testing.T) {
	parsed := mustParse(t, modeOnlyDiff, 1)
	_ = requireReason(
		t,
		parsed.ValidateRightRange("script.sh", 1, 1),
		ReasonTextPatchUnavailable,
	)
}

func TestValidateRightRangeRejectsMissingVerifiedPath(t *testing.T) {
	parsed := mustParse(t, multiHunkDiff, 1)
	err := parsed.ValidateRightRange("missing.txt", 10, 10)
	validationError := requireReason(t, err, ReasonPathNotFound)
	if validationError.Details.Path != "missing.txt" {
		t.Fatalf("Details.Path = %q, want missing.txt", validationError.Details.Path)
	}
}

func TestParseRequiresExactFileCount(t *testing.T) {
	raw := newFileDiff + binaryDiff
	_ = mustParse(t, raw, 2)

	_, err := Parse([]byte(raw), 3)
	validationError := requireReason(t, err, ReasonDiffIncomplete)
	if validationError.Details.ExpectedFileCount != 3 ||
		validationError.Details.ParsedFileCount != 2 {
		t.Fatalf(
			"file count details = expected %d, parsed %d; want 3, 2",
			validationError.Details.ExpectedFileCount,
			validationError.Details.ParsedFileCount,
		)
	}
}

func TestParseEmptyDiffRequiresNoChangedFiles(t *testing.T) {
	_, err := Parse(nil, 0)
	if err != nil {
		t.Fatalf("Parse(nil, 0) error = %v", err)
	}

	_, err = Parse(nil, 1)
	validationError := requireReason(t, err, ReasonDiffIncomplete)
	if validationError.Details.Cause != "empty_diff_for_changed_files" {
		t.Fatalf("Details.Cause = %q, want empty_diff_for_changed_files", validationError.Details.Cause)
	}
}

func TestParseRejectsMalformedAndIncompleteInput(t *testing.T) {
	malformed := `diff --git a/example.txt b/example.txt
index 1111111..2222222 100644
--- a/example.txt
+++ b/example.txt
@@ -1,2 +1,2 @@
-old
+new
`

	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "malformed hunk counts", raw: malformed},
		{name: "missing terminal newline", raw: strings.TrimSuffix(multiHunkDiff, "\n")},
		{name: "unexpected preamble", raw: "unexpected\n" + multiHunkDiff},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.raw), 1)
			validationError := requireReason(t, err, ReasonDiffIncomplete)
			if validationError.Details.Cause == "" {
				t.Fatal("Details.Cause is empty")
			}
		})
	}
}

func TestParseRejectsOverlappingOrOutOfOrderHunks(t *testing.T) {
	overlapping := `diff --git a/example.txt b/example.txt
index 1111111..2222222 100644
--- a/example.txt
+++ b/example.txt
@@ -1 +1 @@
-old one
+new one
@@ -2 +1 @@
-old two
+new two
`
	outOfOrder := `diff --git a/example.txt b/example.txt
index 1111111..2222222 100644
--- a/example.txt
+++ b/example.txt
@@ -5 +5 @@
-old five
+new five
@@ -1 +1 @@
-old one
+new one
`

	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "overlapping", raw: overlapping},
		{name: "out of order", raw: outOfOrder},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.raw), 1)
			_ = requireReason(t, err, ReasonDiffIncomplete)
		})
	}
}

func TestParseRejectsDuplicateRightPaths(t *testing.T) {
	_, err := Parse([]byte(multiHunkDiff+multiHunkDiff), 2)
	validationError := requireReason(t, err, ReasonDiffIncomplete)
	if validationError.Details.Cause != "duplicate_right_path" {
		t.Fatalf("Details.Cause = %q, want duplicate_right_path", validationError.Details.Cause)
	}
}

func TestParseRejectsDiffAtSizeLimit(t *testing.T) {
	raw := make([]byte, MaxDiffBytes)
	_, err := Parse(raw, 1)
	validationError := requireReason(t, err, ReasonDiffTooLarge)
	if validationError.Details.Size != MaxDiffBytes ||
		validationError.Details.Limit != MaxDiffBytes {
		t.Fatalf(
			"size details = %d/%d, want %d/%d",
			validationError.Details.Size,
			validationError.Details.Limit,
			MaxDiffBytes,
			MaxDiffBytes,
		)
	}
}

func TestTooLargeErrorPreservesReasonAndSize(t *testing.T) {
	tooLarge := tooLargeError(MaxDiffBytes + 1)
	_ = requireReason(t, tooLarge, ReasonDiffTooLarge)
	if tooLarge.Details.Size != MaxDiffBytes+1 {
		t.Fatalf("tooLarge Details.Size = %d, want %d", tooLarge.Details.Size, MaxDiffBytes+1)
	}
}

func mustParse(t *testing.T, raw string, expectedFileCount int) Diff {
	t.Helper()

	parsed, err := Parse([]byte(raw), expectedFileCount)
	if err != nil {
		if validationError, ok := errors.AsType[*ValidationError](err); ok {
			t.Fatalf("Parse() error = %v; details = %#v", err, validationError.Details)
		}
		t.Fatalf("Parse() error = %v", err)
	}
	return parsed
}

func requireReason(t *testing.T, err error, reason Reason) *ValidationError {
	t.Helper()

	if err == nil {
		t.Fatalf("error = nil, want reason %q", reason)
	}
	validationError, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if validationError.Details.Reason != reason {
		t.Fatalf("Details.Reason = %q, want %q", validationError.Details.Reason, reason)
	}
	return validationError
}
