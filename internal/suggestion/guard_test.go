package suggestion

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestMarshalReviewRequestDigestUsesCanonicalFieldOrderAndBodyDigests(t *testing.T) {
	t.Parallel()

	startLine := 40
	input := ReviewRequestDigestInput{
		Host:        "github.com",
		Owner:       "octo",
		Repository:  "repo",
		PullRequest: 123,
		BaseSHA:     "base123",
		HeadSHA:     "head123",
		Event:       "COMMENT",
		ReviewBody:  "Two focused fixes.",
		Comments: []ReviewCommentDigestInput{
			{
				Path:      "Sources/Example.swift",
				StartLine: &startLine,
				EndLine:   42,
				Side:      "RIGHT",
				Body:      "A note\n\n```suggestion\nreplacement\n```",
			},
			{
				Path:    "Sources/Other.swift",
				EndLine: 9,
				Side:    "RIGHT",
				Body:    "```suggestion\nother\n```",
			},
		},
	}

	encoded, err := marshalReviewRequestDigest(input)
	if err != nil {
		t.Fatalf("marshalReviewRequestDigest() error = %v", err)
	}

	reviewBodySum := sha256.Sum256([]byte(input.ReviewBody))
	firstBodySum := sha256.Sum256([]byte(input.Comments[0].Body))
	secondBodySum := sha256.Sum256([]byte(input.Comments[1].Body))
	want := fmt.Sprintf(
		`{"digestVersion":1,"host":"github.com","owner":"octo","repository":"repo","pullRequest":123,"baseSHA":"base123","headSHA":"head123","event":"COMMENT","reviewBodySHA256":"%x","comments":[{"path":"Sources/Example.swift","startLine":40,"endLine":42,"side":"RIGHT","bodySHA256":"%x"},{"path":"Sources/Other.swift","endLine":9,"side":"RIGHT","bodySHA256":"%x"}]}`,
		reviewBodySum,
		firstBodySum,
		secondBodySum,
	)
	if string(encoded) != want {
		t.Errorf("marshalReviewRequestDigest() = %s, want %s", encoded, want)
	}
	for _, secret := range []string{"Two focused fixes.", "replacement", "other"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("canonical digest exposes body content %q", secret)
		}
	}
	if strings.HasSuffix(string(encoded), "\n") {
		t.Error("canonical digest has a trailing newline")
	}

	requestSum := sha256.Sum256([]byte(want))
	wantRequestSHA256 := fmt.Sprintf("%x", requestSum)
	gotRequestSHA256, err := ComputeReviewRequestSHA256(input)
	if err != nil {
		t.Fatalf("ComputeReviewRequestSHA256() error = %v", err)
	}
	if gotRequestSHA256 != wantRequestSHA256 {
		t.Errorf(
			"ComputeReviewRequestSHA256() = %q, want %q",
			gotRequestSHA256,
			wantRequestSHA256,
		)
	}
}

func TestMarshalReviewRequestDigestUsesAnEmptyArrayAndOmitsAbsentStartLine(t *testing.T) {
	t.Parallel()

	encoded, err := marshalReviewRequestDigest(ReviewRequestDigestInput{})
	if err != nil {
		t.Fatalf("marshalReviewRequestDigest() error = %v", err)
	}
	if strings.Contains(string(encoded), "startLine") {
		t.Errorf("marshalReviewRequestDigest() = %s, want startLine omitted", encoded)
	}
	if !strings.HasSuffix(string(encoded), `"comments":[]}`) {
		t.Errorf("marshalReviewRequestDigest() = %s, want an empty comments array", encoded)
	}
}

func TestComputeReviewRequestSHA256IsDeterministic(t *testing.T) {
	t.Parallel()

	input := validReviewDigestInput()
	first, err := ComputeReviewRequestSHA256(input)
	if err != nil {
		t.Fatalf("first ComputeReviewRequestSHA256() error = %v", err)
	}
	second, err := ComputeReviewRequestSHA256(input)
	if err != nil {
		t.Fatalf("second ComputeReviewRequestSHA256() error = %v", err)
	}
	if first != second {
		t.Errorf("hashes differ: first = %q, second = %q", first, second)
	}
	if len(first) != sha256.Size*2 {
		t.Errorf("hash length = %d, want %d", len(first), sha256.Size*2)
	}
}

func TestComputeReviewRequestSHA256BindsEveryTopLevelInput(t *testing.T) {
	t.Parallel()

	base := validReviewDigestInput()
	baseHash := computeReviewHash(t, base)
	tests := []struct {
		name   string
		mutate func(*ReviewRequestDigestInput)
	}{
		{name: "host", mutate: func(input *ReviewRequestDigestInput) { input.Host = "example.com" }},
		{name: "owner", mutate: func(input *ReviewRequestDigestInput) { input.Owner = "other" }},
		{
			name: "repository",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Repository = "other"
			},
		},
		{
			name: "pull request",
			mutate: func(input *ReviewRequestDigestInput) {
				input.PullRequest++
			},
		},
		{name: "base SHA", mutate: func(input *ReviewRequestDigestInput) { input.BaseSHA = "other" }},
		{name: "head SHA", mutate: func(input *ReviewRequestDigestInput) { input.HeadSHA = "other" }},
		{name: "event", mutate: func(input *ReviewRequestDigestInput) { input.Event = "APPROVE" }},
		{
			name: "review body",
			mutate: func(input *ReviewRequestDigestInput) {
				input.ReviewBody += " "
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			changed := cloneReviewDigestInput(base)
			test.mutate(&changed)
			if changedHash := computeReviewHash(t, changed); changedHash == baseHash {
				t.Errorf("hash unchanged after changing %s", test.name)
			}
		})
	}
}

func TestComputeReviewRequestSHA256BindsOrderedCommentInputs(t *testing.T) {
	t.Parallel()

	base := validReviewDigestInput()
	baseHash := computeReviewHash(t, base)
	otherStartLine := 7
	tests := []struct {
		name   string
		mutate func(*ReviewRequestDigestInput)
	}{
		{
			name: "path",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Comments[0].Path = "other.go"
			},
		},
		{
			name: "start line",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Comments[0].StartLine = &otherStartLine
			},
		},
		{
			name: "absent start line",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Comments[0].StartLine = nil
			},
		},
		{
			name: "end line",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Comments[0].EndLine++
			},
		},
		{
			name: "side",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Comments[0].Side = "LEFT"
			},
		},
		{
			name: "body",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Comments[0].Body += " "
			},
		},
		{
			name: "comment order",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Comments[0], input.Comments[1] = input.Comments[1], input.Comments[0]
			},
		},
		{
			name: "comment count",
			mutate: func(input *ReviewRequestDigestInput) {
				input.Comments = input.Comments[:1]
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			changed := cloneReviewDigestInput(base)
			test.mutate(&changed)
			if changedHash := computeReviewHash(t, changed); changedHash == baseHash {
				t.Errorf("hash unchanged after changing %s", test.name)
			}
		})
	}
}

func TestBodySHA256UsesExactBytes(t *testing.T) {
	t.Parallel()

	body := "café\r\n"
	sum := sha256.Sum256([]byte(body))
	want := fmt.Sprintf("%x", sum)
	if got := BodySHA256(body); got != want {
		t.Errorf("BodySHA256() = %q, want %q", got, want)
	}
}

func validReviewDigestInput() ReviewRequestDigestInput {
	startLine := 8
	return ReviewRequestDigestInput{
		Host:        "github.com",
		Owner:       "octo",
		Repository:  "repo",
		PullRequest: 7,
		BaseSHA:     "base",
		HeadSHA:     "head",
		Event:       "COMMENT",
		ReviewBody:  "Summary",
		Comments: []ReviewCommentDigestInput{
			{
				Path:      "main.go",
				StartLine: &startLine,
				EndLine:   10,
				Side:      "RIGHT",
				Body:      "```suggestion\nreplacement\n```",
			},
			{
				Path:    "other.go",
				EndLine: 20,
				Side:    "RIGHT",
				Body:    "```suggestion\nother\n```",
			},
		},
	}
}

func cloneReviewDigestInput(input ReviewRequestDigestInput) ReviewRequestDigestInput {
	cloned := input
	cloned.Comments = make([]ReviewCommentDigestInput, len(input.Comments))
	copy(cloned.Comments, input.Comments)
	for index := range cloned.Comments {
		if input.Comments[index].StartLine == nil {
			continue
		}
		value := *input.Comments[index].StartLine
		cloned.Comments[index].StartLine = &value
	}
	return cloned
}

func computeReviewHash(t *testing.T, input ReviewRequestDigestInput) string {
	t.Helper()
	digest, err := ComputeReviewRequestSHA256(input)
	if err != nil {
		t.Fatalf("ComputeReviewRequestSHA256() error = %v", err)
	}
	return digest
}
