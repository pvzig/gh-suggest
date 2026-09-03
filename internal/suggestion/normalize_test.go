package suggestion

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeReplacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     []byte
		content   string
		byteCount int
		lineCount int
	}{
		{
			name:      "empty deletion",
			input:     []byte{},
			content:   "",
			byteCount: 0,
			lineCount: 0,
		},
		{
			name:      "single terminal LF",
			input:     []byte("let value = 1\n"),
			content:   "let value = 1",
			byteCount: 13,
			lineCount: 1,
		},
		{
			name:      "single LF becomes deletion",
			input:     []byte("\n"),
			content:   "",
			byteCount: 0,
			lineCount: 0,
		},
		{
			name:      "CRLF and one terminal LF",
			input:     []byte("one\r\ntwo\r\n\r\n"),
			content:   "one\ntwo\n",
			byteCount: 8,
			lineCount: 3,
		},
		{
			name:      "additional trailing blank line preserved",
			input:     []byte("one\n\n"),
			content:   "one\n",
			byteCount: 4,
			lineCount: 2,
		},
		{
			name:      "lone carriage return preserved",
			input:     []byte("one\rtwo"),
			content:   "one\rtwo",
			byteCount: 7,
			lineCount: 1,
		},
		{
			name:      "UTF-8 byte count",
			input:     []byte("café\n"),
			content:   "café",
			byteCount: 5,
			lineCount: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			replacement, err := NormalizeReplacement(test.input)
			if err != nil {
				t.Fatalf("NormalizeReplacement() error = %v", err)
			}

			if replacement.content != test.content {
				t.Errorf("content = %q, want %q", replacement.content, test.content)
			}

			metadata := replacement.Metadata()
			if metadata.ByteCount != test.byteCount {
				t.Errorf("ByteCount = %d, want %d", metadata.ByteCount, test.byteCount)
			}
			if metadata.LineCount != test.lineCount {
				t.Errorf("LineCount = %d, want %d", metadata.LineCount, test.lineCount)
			}

			sum := sha256.Sum256([]byte(test.content))
			wantSHA256 := fmt.Sprintf("%x", sum)
			if metadata.SHA256 != wantSHA256 {
				t.Errorf("SHA256 = %q, want %q", metadata.SHA256, wantSHA256)
			}
		})
	}
}

func TestNormalizeReplacementRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   []byte
		wantErr error
	}{
		{
			name:    "invalid UTF-8",
			input:   []byte{0xff},
			wantErr: ErrInvalidUTF8,
		},
		{
			name:    "NUL",
			input:   []byte("before\x00after"),
			wantErr: ErrContainsNUL,
		},
		{
			name:    "sanitizer-mutated C0 control",
			input:   []byte("before\x1bafter"),
			wantErr: ErrUnstableControlCharacter,
		},
		{
			name:    "sanitizer-mutated C1 control",
			input:   []byte("before\u0085after"),
			wantErr: ErrUnstableControlCharacter,
		},
		{
			name:    "sanitizer-mutated literal JSON control escape",
			input:   []byte(`before\u001bafter`),
			wantErr: ErrUnstableControlCharacter,
		},
		{
			name:    "too large",
			input:   []byte(strings.Repeat("a", MaxReplacementBytes+1)),
			wantErr: ErrTooLarge,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NormalizeReplacement(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NormalizeReplacement() error = %v, want %v", err, test.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "replacement: ") {
				t.Errorf("error = %q, want replacement field context", err)
			}
		})
	}
}

func TestNormalizeReplacementAllowsLiteralSafeOrUnrecognizedJSONEscapes(t *testing.T) {
	t.Parallel()

	for _, input := range []string{`literal \u000a`, `literal \u001B`, `literal \U001b`} {
		replacement, err := NormalizeReplacement([]byte(input))
		if err != nil {
			t.Fatalf("NormalizeReplacement(%q) error = %v", input, err)
		}
		if replacement.content != input {
			t.Fatalf("content = %q, want %q", replacement.content, input)
		}
	}
}

func TestNormalizeReplacementAppliesLimitAfterCRLFNormalization(t *testing.T) {
	t.Parallel()

	input := []byte(strings.Repeat("\r\n", (MaxReplacementBytes/2)+1))
	replacement, err := NormalizeReplacement(input)
	if err != nil {
		t.Fatalf("NormalizeReplacement() error = %v", err)
	}

	if replacement.Metadata().ByteCount != MaxReplacementBytes/2 {
		t.Errorf(
			"ByteCount = %d, want %d",
			replacement.Metadata().ByteCount,
			MaxReplacementBytes/2,
		)
	}
}

func TestNormalizeReplacementAppliesLimitAfterRemovingTerminalLF(t *testing.T) {
	t.Parallel()

	input := []byte(strings.Repeat("a", MaxReplacementBytes) + "\n")
	replacement, err := NormalizeReplacement(input)
	if err != nil {
		t.Fatalf("NormalizeReplacement() error = %v", err)
	}

	if replacement.Metadata().ByteCount != MaxReplacementBytes {
		t.Errorf(
			"ByteCount = %d, want %d",
			replacement.Metadata().ByteCount,
			MaxReplacementBytes,
		)
	}
}

func TestNormalizeReplacementAcceptsResponseStableWhitespaceControls(t *testing.T) {
	t.Parallel()

	replacement, err := NormalizeReplacement([]byte("tab\tline\nvertical\vcarriage\r"))
	if err != nil {
		t.Fatalf("NormalizeReplacement() error = %v", err)
	}
	if replacement.content != "tab\tline\nvertical\vcarriage\r" {
		t.Errorf("content = %q", replacement.content)
	}
}

func TestReplacementDoesNotAliasInput(t *testing.T) {
	t.Parallel()

	input := []byte("safe")
	replacement, err := NormalizeReplacement(input)
	if err != nil {
		t.Fatalf("NormalizeReplacement() error = %v", err)
	}

	input[0] = 'x'

	if replacement.content != "safe" {
		t.Errorf("content = %q, want immutable content", replacement.content)
	}
}

func TestExceedsReplacementLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []byte
		want  bool
	}{
		{name: "empty", input: nil, want: false},
		{name: "at the limit", input: []byte(strings.Repeat("a", MaxReplacementBytes)), want: false},
		{
			name:  "at the limit with a terminal newline",
			input: []byte(strings.Repeat("a", MaxReplacementBytes) + "\n"),
			want:  false,
		},
		{
			name:  "at the limit with a terminal CRLF",
			input: []byte(strings.Repeat("a", MaxReplacementBytes) + "\r\n"),
			want:  false,
		},
		{name: "over the limit", input: []byte(strings.Repeat("a", MaxReplacementBytes+1)), want: true},
		{
			name:  "over the limit only before CRLF normalization",
			input: []byte(strings.Repeat("a\r\n", (MaxReplacementBytes+3)/3)),
			want:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := ExceedsReplacementLimit(test.input); got != test.want {
				t.Errorf("ExceedsReplacementLimit() = %t, want %t", got, test.want)
			}
			_, err := NormalizeReplacement(test.input)
			if errors.Is(err, ErrTooLarge) != test.want {
				t.Errorf("NormalizeReplacement() error = %v, want ErrTooLarge = %t", err, test.want)
			}
		})
	}
}

func TestNormalizeNote(t *testing.T) {
	t.Parallel()

	note, err := NormalizeNote("**Reason**\r\n\r\nKeep `Task` cancellation.")
	if err != nil {
		t.Fatalf("NormalizeNote() error = %v", err)
	}

	const want = "**Reason**\n\nKeep `Task` cancellation."
	if note.Content() != want {
		t.Errorf("Content() = %q, want %q", note.Content(), want)
	}
	if note.Empty() {
		t.Error("Empty() = true, want false")
	}

	empty, err := NormalizeNote("")
	if err != nil {
		t.Fatalf("NormalizeNote(empty) error = %v", err)
	}
	if !empty.Empty() {
		t.Error("Empty() = false, want true")
	}
}

func TestNormalizeNoteAcceptsBalancedFences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "closed backtick fence", input: "Before:\n```\nlet value = 1\n```"},
		{name: "closed tilde fence", input: "Before:\n~~~\nlet value = 1\n~~~"},
		{name: "info string on the opening fence", input: "```swift\nlet value = 1\n```"},
		{name: "longer closing fence", input: "```\nlet value = 1\n`````"},
		{name: "backticks inside an opening fence info string", input: "``` `a` ```"},
		{name: "inline code span", input: "Keep `Task` cancellation."},
		{name: "indented code block", input: "Before:\n\n    let value = 1"},
		{name: "fence inside a block quote", input: "> ```\n> let value = 1"},
		{name: "fewer than three backticks", input: "``\nnot a fence\n``"},
		{name: "closed pre block", input: "<pre>\nraw\n</PRE>"},
		{name: "closed comment", input: "<!--\ncomment\n-->"},
		{name: "closed processing instruction", input: "<?instruction?>"},
		{name: "closed declaration", input: "<!DOCTYPE html>"},
		{name: "closed CDATA", input: "<![CDATA[raw]]>"},
		{name: "blank-line-terminated HTML block", input: "<div>\nraw"},
		{name: "raw HTML inside code fence", input: "```html\n<pre>\n```"},
		{name: "fence inside closed comment", input: "<!--\n```\n-->"},
		{name: "fence inside closed pre block", input: "<pre>\n~~~\n</pre>"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NormalizeNote(test.input); err != nil {
				t.Fatalf("NormalizeNote() error = %v", err)
			}
		})
	}
}

func TestNormalizeNoteRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{
			name:    "invalid UTF-8",
			input:   string([]byte{0xff}),
			wantErr: ErrInvalidUTF8,
		},
		{
			name:    "NUL",
			input:   "before\x00after",
			wantErr: ErrContainsNUL,
		},
		{
			name:    "sanitizer-mutated control",
			input:   "before\x1bafter",
			wantErr: ErrUnstableControlCharacter,
		},
		{
			name:    "too large",
			input:   strings.Repeat("a", MaxNoteBytes+1),
			wantErr: ErrTooLarge,
		},
		{
			name:    "unclosed backtick fence",
			input:   "Broken:\n```\nlet value = 1",
			wantErr: ErrUnclosedCodeFence,
		},
		{
			name:    "unclosed tilde fence",
			input:   "Broken:\n~~~\nlet value = 1",
			wantErr: ErrUnclosedCodeFence,
		},
		{
			name:    "closing fence is shorter than its opening",
			input:   "Broken:\n````\nlet value = 1\n```",
			wantErr: ErrUnclosedCodeFence,
		},
		{
			name:    "closing fence carries an info string",
			input:   "Broken:\n```swift\nlet value = 1\n```swift",
			wantErr: ErrUnclosedCodeFence,
		},
		{
			name:    "unclosed pre block",
			input:   "<pre>\nraw",
			wantErr: ErrUnclosedHTMLBlock,
		},
		{
			name:    "self-closing pre still opens a raw block",
			input:   "<pre/>",
			wantErr: ErrUnclosedHTMLBlock,
		},
		{
			name:    "unclosed script block with attributes",
			input:   "  <SCRIPT type=\"text/javascript\">\nraw",
			wantErr: ErrUnclosedHTMLBlock,
		},
		{
			name:    "unclosed comment",
			input:   "<!--\ncomment",
			wantErr: ErrUnclosedHTMLBlock,
		},
		{
			name:    "unclosed comment containing a fence",
			input:   "<!--\n```",
			wantErr: ErrUnclosedHTMLBlock,
		},
		{
			name:    "unclosed processing instruction",
			input:   "<?instruction",
			wantErr: ErrUnclosedHTMLBlock,
		},
		{
			name:    "unclosed declaration",
			input:   "<!DOCTYPE html",
			wantErr: ErrUnclosedHTMLBlock,
		},
		{
			name:    "unclosed CDATA",
			input:   "<![CDATA[raw",
			wantErr: ErrUnclosedHTMLBlock,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NormalizeNote(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NormalizeNote() error = %v, want %v", err, test.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "note: ") {
				t.Errorf("error = %q, want note field context", err)
			}
		})
	}
}

func TestNormalizeNoteLimitIsInclusive(t *testing.T) {
	t.Parallel()

	note, err := NormalizeNote(strings.Repeat("a", MaxNoteBytes))
	if err != nil {
		t.Fatalf("NormalizeNote() error = %v", err)
	}
	if len(note.Content()) != MaxNoteBytes {
		t.Errorf("note length = %d, want %d", len(note.Content()), MaxNoteBytes)
	}
}

func TestNormalizeReviewBodyNormalizesWithoutApplyingNoteFenceRules(t *testing.T) {
	t.Parallel()

	body, err := NormalizeReviewBody("Summary\r\n\r\n```\r\nopen")
	if err != nil {
		t.Fatalf("NormalizeReviewBody() error = %v", err)
	}
	if body.Content() != "Summary\n\n```\nopen" {
		t.Errorf("Content() = %q", body.Content())
	}
	if body.Content() == "" {
		t.Error("Content() is empty")
	}

	empty, err := NormalizeReviewBody("")
	if err != nil {
		t.Fatalf("NormalizeReviewBody(empty) error = %v", err)
	}
	if empty.Content() != "" {
		t.Errorf("Content() = %q, want empty", empty.Content())
	}
}

func TestNormalizeReviewBodyRejectsInvalidInputAndEnforcesLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{name: "invalid UTF-8", input: string([]byte{0xff}), wantErr: ErrInvalidUTF8},
		{name: "NUL", input: "before\x00after", wantErr: ErrContainsNUL},
		{
			name:    "sanitizer-mutated control",
			input:   "before\x1bafter",
			wantErr: ErrUnstableControlCharacter,
		},
		{name: "too large", input: strings.Repeat("a", MaxNoteBytes+1), wantErr: ErrTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NormalizeReviewBody(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NormalizeReviewBody() error = %v, want %v", err, test.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "review body: ") {
				t.Errorf("error = %q, want review body field context", err)
			}
		})
	}

	body, err := NormalizeReviewBody(strings.Repeat("a", MaxNoteBytes))
	if err != nil {
		t.Fatalf("NormalizeReviewBody(at limit) error = %v", err)
	}
	if len(body.Content()) != MaxNoteBytes {
		t.Errorf("review body length = %d, want %d", len(body.Content()), MaxNoteBytes)
	}
}

func TestRenderMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		note        string
		replacement string
		want        string
	}{
		{
			name:        "replacement without note",
			replacement: "let value = 1\nreturn value",
			want:        "```suggestion\nlet value = 1\nreturn value\n```",
		},
		{
			name:        "Markdown note",
			note:        "**Why:** preserve `Task` cancellation.",
			replacement: "try await operation()",
			want:        "**Why:** preserve `Task` cancellation.\n\n```suggestion\ntry await operation()\n```",
		},
		{
			name: "empty replacement deletion",
			note: "Delete this.",
			want: "Delete this.\n\n```suggestion\n```",
		},
		{
			name:        "triple backticks in replacement",
			replacement: "let markdown = \"```swift\"",
			want:        "````suggestion\nlet markdown = \"```swift\"\n````",
		},
		{
			name:        "long backtick run",
			replacement: "before `````` after",
			want:        "```````suggestion\nbefore `````` after\n```````",
		},
		{
			name:        "note backticks do not affect fence",
			note:        "The ````` note is prose.",
			replacement: "value",
			want:        "The ````` note is prose.\n\n```suggestion\nvalue\n```",
		},
		{
			name:        "preserved replacement trailing blank line",
			replacement: "value\n",
			want:        "```suggestion\nvalue\n\n```",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			replacement := Replacement{content: test.replacement}
			note := Note{content: test.note}
			if got := RenderMarkdown(note, replacement); got != test.want {
				t.Errorf("RenderMarkdown() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFenceLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		content string
		want    int
	}{
		{content: "", want: 3},
		{content: "`", want: 3},
		{content: "``", want: 3},
		{content: "```", want: 4},
		{content: "`` a ````", want: 5},
		{content: "`a``b```c``", want: 4},
	}

	for _, test := range tests {
		if got := fenceLength(test.content); got != test.want {
			t.Errorf("fenceLength(%q) = %d, want %d", test.content, got, test.want)
		}
	}
}
