// Package suggestion normalizes replacement input and renders GitHub
// suggestion Markdown.
package suggestion

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// MaxReplacementBytes is the maximum normalized replacement size.
	MaxReplacementBytes = 1 << 20
	// MaxNoteBytes is the maximum normalized note size.
	MaxNoteBytes = 64 << 10
)

var (
	// ErrInvalidUTF8 indicates that input is not valid UTF-8.
	ErrInvalidUTF8 = errors.New("input is not valid UTF-8")
	// ErrContainsNUL indicates that input contains a NUL byte.
	ErrContainsNUL = errors.New("input contains a NUL byte")
	// ErrUnstableControlCharacter indicates that input contains a control
	// character that go-gh sanitizes when reading the content back from GitHub.
	ErrUnstableControlCharacter = errors.New("input contains a control character that cannot be reconciled exactly")
	// ErrTooLarge indicates that normalized input exceeds its size limit.
	ErrTooLarge = errors.New("input exceeds the size limit")
	// ErrUnclosedCodeFence indicates that input ends inside a fenced code
	// block.
	ErrUnclosedCodeFence = errors.New("input leaves a fenced code block open")
	// ErrUnclosedHTMLBlock indicates that input ends inside a raw HTML block
	// whose termination requires an explicit closing sequence.
	ErrUnclosedHTMLBlock = errors.New("input leaves a raw HTML block open")
)

// Replacement is validated replacement text in the exact normalized form that
// is embedded in Markdown and hashed. Its content cannot be mutated.
type Replacement struct {
	content string
}

// ReplacementMetadata describes the normalized bytes without exposing their
// content.
type ReplacementMetadata struct {
	ByteCount int    `json:"byteCount"`
	LineCount int    `json:"lineCount"`
	SHA256    string `json:"sha256"`
}

// Note is validated, CRLF-normalized explanatory prose. Its content cannot be
// mutated.
type Note struct {
	content string
}

// ReviewBody is validated, CRLF-normalized review summary prose. Unlike Note,
// it is not followed by a suggestion block, so a terminal open Markdown fence
// does not affect applicability and is allowed.
type ReviewBody struct {
	content string
}

// NormalizeReplacement validates and normalizes replacement input.
//
// CRLF pairs become LF, then exactly one terminal LF is removed. Additional
// trailing blank lines are preserved. Empty normalized content represents a
// deletion.
func NormalizeReplacement(input []byte) (Replacement, error) {
	normalized, err := normalize(input, "replacement")
	if err != nil {
		return Replacement{}, err
	}

	normalized = bytes.TrimSuffix(normalized, []byte{'\n'})
	if err := validateSize(normalized, "replacement", MaxReplacementBytes); err != nil {
		return Replacement{}, err
	}

	return Replacement{content: string(normalized)}, nil
}

// ExceedsReplacementLimit reports whether input would exceed the replacement
// size limit once normalized. It measures the normalized length without
// allocating a normalized copy, so callers can reject oversized input before
// paying for validation.
func ExceedsReplacementLimit(input []byte) bool {
	size := len(input) - bytes.Count(input, []byte{'\r', '\n'})
	if size > 0 && input[len(input)-1] == '\n' {
		size--
	}

	return size > MaxReplacementBytes
}

// NormalizeNote validates a note and normalizes CRLF pairs to LF.
//
// A note that ends inside a fenced code block or explicitly terminated raw HTML
// block is rejected: Markdown would absorb the suggestion block that follows
// it, producing inert content with no apply control.
func NormalizeNote(input string) (Note, error) {
	normalized, err := normalize([]byte(input), "note")
	if err != nil {
		return Note{}, err
	}
	if err := validateSize(normalized, "note", MaxNoteBytes); err != nil {
		return Note{}, err
	}
	switch unclosedMarkdownBlock(string(normalized)) {
	case markdownCodeFence:
		return Note{}, fmt.Errorf("note: %w", ErrUnclosedCodeFence)
	case markdownRawHTML:
		return Note{}, fmt.Errorf("note: %w", ErrUnclosedHTMLBlock)
	}

	return Note{content: string(normalized)}, nil
}

// NormalizeReviewBody validates and CRLF-normalizes review summary prose.
func NormalizeReviewBody(input string) (ReviewBody, error) {
	normalized, err := normalize([]byte(input), "review body")
	if err != nil {
		return ReviewBody{}, err
	}
	if err := validateSize(normalized, "review body", MaxNoteBytes); err != nil {
		return ReviewBody{}, err
	}

	return ReviewBody{content: string(normalized)}, nil
}

// Metadata returns metadata computed from the exact normalized replacement
// bytes.
func (replacement Replacement) Metadata() ReplacementMetadata {
	content := []byte(replacement.content)
	lineCount := 0
	if len(content) > 0 {
		lineCount = 1 + bytes.Count(content, []byte{'\n'})
	}

	return ReplacementMetadata{
		ByteCount: len(content),
		LineCount: lineCount,
		SHA256:    sha256Hex(content),
	}
}

// Content returns the normalized note.
func (note Note) Content() string {
	return note.content
}

// Empty reports whether the normalized note is empty.
func (note Note) Empty() bool {
	return note.content == ""
}

// Content returns the normalized review summary.
func (body ReviewBody) Content() string {
	return body.content
}

// RenderMarkdown constructs the exact GitHub review comment body.
//
// The fence is at least three backticks and is one backtick longer than the
// longest consecutive run in the replacement. An empty replacement renders an
// empty block, which GitHub applies as a deletion of the selected lines; a
// blank line inside the block would instead blank them.
func RenderMarkdown(note Note, replacement Replacement) string {
	fence := strings.Repeat("`", fenceLength(replacement.content))

	var body strings.Builder
	body.Grow(len(note.content) + len(replacement.content) + (2 * len(fence)) + 14)
	if !note.Empty() {
		body.WriteString(note.content)
		body.WriteString("\n\n")
	}
	body.WriteString(fence)
	body.WriteString("suggestion\n")
	if replacement.content != "" {
		body.WriteString(replacement.content)
		body.WriteByte('\n')
	}
	body.WriteString(fence)

	return body.String()
}

func normalize(input []byte, field string) ([]byte, error) {
	if !utf8.Valid(input) {
		return nil, fmt.Errorf("%s: %w", field, ErrInvalidUTF8)
	}
	if bytes.IndexByte(input, 0) >= 0 {
		return nil, fmt.Errorf("%s: %w", field, ErrContainsNUL)
	}
	if containsUnstableControlCharacter(string(input)) {
		return nil, fmt.Errorf("%s: %w", field, ErrUnstableControlCharacter)
	}

	return bytes.ReplaceAll(input, []byte{'\r', '\n'}, []byte{'\n'}), nil
}

// containsUnstableControlCharacter mirrors the C0 and C1 character set that
// go-gh's JSON response sanitizer converts to caret notation. Rejecting those
// characters before hashing keeps ambiguous-write reconciliation byte-exact.
func containsUnstableControlCharacter(content string) bool {
	for _, character := range content {
		if (character >= '\x01' && character <= '\x08') ||
			character == '\x0c' ||
			(character >= '\x0e' && character <= '\x1f') ||
			(character >= '\u0080' && character <= '\u009f') {
			return true
		}
	}
	return containsUnstableJSONControlEscape(content)
}

// containsUnstableJSONControlEscape detects ordinary source text that go-gh's
// JSON sanitizer mistakes for an encoded control character after json.Marshal
// escapes the source backslash. Such text is changed on read-back even though
// it contains no actual control rune.
func containsUnstableJSONControlEscape(content string) bool {
	for index := 0; index+6 <= len(content); index++ {
		candidate := content[index : index+6]
		if !strings.HasPrefix(candidate, `\u00`) {
			continue
		}
		value, ok := lowerHexByte(candidate[4], candidate[5])
		if !ok {
			continue
		}
		if value <= 0x1f {
			switch value {
			case 0x09, 0x0a, 0x0b, 0x0d:
				continue
			default:
				return true
			}
		}
		if value >= 0x80 && value <= 0x9f {
			return true
		}
	}
	return false
}

func lowerHexByte(high byte, low byte) (byte, bool) {
	highValue, highOK := lowerHexNibble(high)
	lowValue, lowOK := lowerHexNibble(low)
	return (highValue << 4) | lowValue, highOK && lowOK
}

func lowerHexNibble(character byte) (byte, bool) {
	switch {
	case character >= '0' && character <= '9':
		return character - '0', true
	case character >= 'a' && character <= 'f':
		return character - 'a' + 10, true
	default:
		return 0, false
	}
}

func validateSize(input []byte, field string, limit int) error {
	if len(input) > limit {
		return fmt.Errorf(
			"%s: %w: got %d bytes after normalization, maximum is %d",
			field,
			ErrTooLarge,
			len(input),
			limit,
		)
	}

	return nil
}

type markdownBlock uint8

const (
	markdownBlockNone markdownBlock = iota
	markdownCodeFence
	markdownRawHTML
)

// unclosedMarkdownBlock reports which top-level GFM block, if any, remains open
// at the end of content. One state machine ensures fence markers inside raw HTML
// and raw HTML markers inside fences remain inert.
//
// Only top-level fences are tracked: a fence opened inside a block quote or a
// list item is closed by the blank line that separates the note from the
// suggestion block. Raw HTML forms that end at a blank line are safe for the
// same reason and do not need explicit state here.
func unclosedMarkdownBlock(content string) markdownBlock {
	var (
		block           markdownBlock
		codeFenceMarker byte
		codeFenceWidth  int
		htmlEnd         string
		htmlFoldCase    bool
	)

	for line := range strings.SplitSeq(content, "\n") {
		if block == markdownRawHTML {
			if containsHTMLBlockEnd(line, htmlEnd, htmlFoldCase) {
				block = markdownBlockNone
			}
			continue
		}

		lineMarker, lineWidth, info := fenceLine(line)
		if block == markdownCodeFence {
			if lineMarker == codeFenceMarker &&
				lineWidth >= codeFenceWidth &&
				strings.TrimSpace(info) == "" {
				block = markdownBlockNone
			}
			continue
		}
		if lineWidth != 0 &&
			(lineMarker != '`' || !strings.ContainsRune(info, '`')) {
			block, codeFenceMarker, codeFenceWidth = markdownCodeFence, lineMarker, lineWidth
			continue
		}

		var found bool
		htmlEnd, htmlFoldCase, found = rawHTMLBlockEnd(line)
		if found && !containsHTMLBlockEnd(line, htmlEnd, htmlFoldCase) {
			block = markdownRawHTML
		}
	}

	return block
}

// rawHTMLBlockEnd returns the explicit terminator for GFM HTML block types
// 1-5. A line may be indented by at most three spaces to begin a block.
func rawHTMLBlockEnd(line string) (string, bool, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 {
		return "", false, false
	}
	line = line[indent:]
	lowerLine := strings.ToLower(line)
	for _, tag := range []string{"script", "pre", "style", "textarea"} {
		opening := "<" + tag
		if strings.HasPrefix(lowerLine, opening) && htmlTagBoundary(line, len(opening)) {
			return "</" + tag + ">", true, true
		}
	}

	switch {
	case strings.HasPrefix(line, "<!--"):
		return "-->", false, true
	case strings.HasPrefix(line, "<?"):
		return "?>", false, true
	case strings.HasPrefix(line, "<![CDATA["):
		return "]]>", false, true
	case len(line) > 2 && strings.HasPrefix(line, "<!") && line[2] >= 'A' && line[2] <= 'Z':
		return ">", false, true
	default:
		return "", false, false
	}
}

func htmlTagBoundary(line string, index int) bool {
	return index == len(line) ||
		line[index] == ' ' ||
		line[index] == '\t' ||
		line[index] == '>' ||
		(line[index] == '/' && index+1 < len(line) && line[index+1] == '>')
}

func containsHTMLBlockEnd(line string, end string, foldCase bool) bool {
	if foldCase {
		return strings.Contains(strings.ToLower(line), end)
	}
	return strings.Contains(line, end)
}

// fenceLine reports the fence character, its run length, and the trailing info
// string for a code-fence line. A run length of zero means the line does not
// open or close a fence.
func fenceLine(line string) (byte, int, string) {
	index := 0
	for index < len(line) && line[index] == ' ' {
		index++
	}
	if index > 3 || index == len(line) {
		return 0, 0, ""
	}
	marker := line[index]
	if marker != '`' && marker != '~' {
		return 0, 0, ""
	}
	width := 0
	for index < len(line) && line[index] == marker {
		width++
		index++
	}
	if width < 3 {
		return 0, 0, ""
	}

	return marker, width, line[index:]
}

func fenceLength(content string) int {
	longest := 0
	current := 0
	for index := 0; index < len(content); index++ {
		if content[index] == '`' {
			current++
			longest = max(longest, current)
			continue
		}
		current = 0
	}

	return max(3, longest+1)
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum)
}
