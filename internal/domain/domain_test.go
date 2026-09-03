package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRepositoryStringOmitsHost(t *testing.T) {
	t.Parallel()

	repository := Repository{Host: SupportedHost, Owner: "owner", Name: "repo"}
	if got := repository.String(); got != "owner/repo" {
		t.Fatalf("String() = %q, want %q", got, "owner/repo")
	}
}

func TestSupportedHostMatchingIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	if !IsSupportedHost("GITHUB.COM") || IsSupportedHost("enterprise.example") {
		t.Fatal("supported-host policy did not match github.com case-insensitively")
	}
}

func TestValidHexDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "lowercase", value: "abcdef01", want: true},
		{name: "uppercase", value: "ABCDEF01", want: true},
		{name: "mixed case", value: "AbCdEf01", want: true},
		{name: "empty", value: "", want: false},
		{name: "odd length", value: "abc", want: false},
		{name: "non-hex", value: "zzzz", want: false},
		{name: "leading sign", value: "+abc", want: false},
		{name: "embedded space", value: "ab cd", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := ValidHexDigest(test.value); got != test.want {
				t.Fatalf("ValidHexDigest(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestValidCommitSHARequiresFullLength(t *testing.T) {
	t.Parallel()

	full := strings.Repeat("a", CommitSHALength)
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "full sha", value: full, want: true},
		{name: "abbreviated", value: full[:8], want: false},
		{name: "too long", value: full + "aa", want: false},
		{name: "non-hex", value: strings.Repeat("z", CommitSHALength), want: false},
		{name: "empty", value: "", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := ValidCommitSHA(test.value); got != test.want {
				t.Fatalf("ValidCommitSHA(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestValidSHA256HexRequiresDigestLength(t *testing.T) {
	t.Parallel()

	digest := strings.Repeat("0", SHA256HexLength)
	if !ValidSHA256Hex(digest) {
		t.Fatalf("ValidSHA256Hex(%q) = false, want true", digest)
	}
	if ValidSHA256Hex(digest[:SHA256HexLength-2]) {
		t.Fatal("ValidSHA256Hex() accepted a short digest")
	}
}

func TestValidRepositoryOwner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  bool
	}{
		{value: "octo-org", want: true},
		{value: "Owner123", want: true},
		{value: "", want: false},
		{value: "-owner", want: false},
		{value: "owner-", want: false},
		{value: "own--er", want: false},
		{value: "managed_user", want: true},
		{value: strings.Repeat("o", 40), want: false},
	}
	for _, test := range tests {
		if got := ValidRepositoryOwner(test.value); got != test.want {
			t.Errorf("ValidRepositoryOwner(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestValidRepositoryName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  bool
	}{
		{value: "octo.repo_v1", want: true},
		{value: "", want: false},
		{value: ".", want: false},
		{value: "..", want: false},
		{value: "repo/name", want: false},
		{value: "repo?query", want: false},
		{value: strings.Repeat("r", 101), want: false},
	}
	for _, test := range tests {
		if got := ValidRepositoryName(test.value); got != test.want {
			t.Errorf("ValidRepositoryName(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestValidRepositoryPathRejectsEscapesAndUncleanForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "nested file", value: "Sources/Example.swift", want: true},
		{name: "root file", value: "README.md", want: true},
		{name: "dotfile", value: ".gitignore", want: true},
		{name: "empty", value: "", want: false},
		{name: "absolute", value: "/etc/passwd", want: false},
		{name: "current directory", value: ".", want: false},
		{name: "parent directory", value: "..", want: false},
		{name: "escaping prefix", value: "../secret", want: false},
		{name: "embedded traversal", value: "a/../b", want: false},
		{name: "trailing slash", value: "dir/", want: false},
		{name: "double slash", value: "a//b", want: false},
		{name: "unclean dot segment", value: "./a", want: false},
		{name: "nul byte", value: "Sources/Example\x00.swift", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := ValidRepositoryPath(test.value); got != test.want {
				t.Fatalf("ValidRepositoryPath(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestContextCancellationFailureRequiresEndedCallerContext(t *testing.T) {
	t.Parallel()

	if got := ContextCancellationFailure(
		context.Background(),
		context.DeadlineExceeded,
		"cancelled",
	); got != nil {
		t.Fatalf("live context failure = %#v, want nil", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cause := errors.New("operation interrupted")
	failure := ContextCancellationFailure(ctx, cause, "cancelled")
	if failure == nil || failure.Code != CodeCancelled {
		t.Fatalf("cancelled context failure = %#v", failure)
	}
	if !errors.Is(failure, cause) || !errors.Is(failure, context.Canceled) {
		t.Fatalf("failure cause = %v, want operation and context causes", failure)
	}
}

func TestCodeKnownCoversTheDocumentedVocabulary(t *testing.T) {
	t.Parallel()

	documented := []Code{
		CodeInvalidArguments,
		CodeAuthenticationFailed,
		CodeTargetNotFound,
		CodeRangeNotCommentable,
		CodeStalePullRequest,
		CodeGitHubAPIError,
		CodeIOError,
		CodeRateLimited,
		CodeWriteOutcomeUnknown,
		CodeUnsupportedHost,
		CodeCancelled,
		CodePermissionDenied,
	}
	for _, code := range documented {
		if !code.Known() {
			t.Errorf("Code(%q).Known() = false, want true", code)
		}
	}

	for _, code := range []Code{"", "made_up", "GITHUB_API_ERROR"} {
		if code.Known() {
			t.Errorf("Code(%q).Known() = true, want false", code)
		}
	}
}

func TestFailureErrorIncludesCauseWhenPresent(t *testing.T) {
	t.Parallel()

	bare := NewFailure(CodeIOError, "It failed.", nil, nil)
	if got := bare.Error(); got != "It failed." {
		t.Fatalf("Error() = %q, want %q", got, "It failed.")
	}

	cause := errors.New("disk on fire")
	wrapped := NewFailure(CodeIOError, "It failed.", nil, cause)
	if got := wrapped.Error(); got != "It failed.: disk on fire" {
		t.Fatalf("Error() = %q, want it to include the cause", got)
	}
	if !errors.Is(wrapped, cause) {
		t.Fatal("errors.Is() could not reach the cause through Unwrap")
	}
}

func TestAsFailureFindsAWrappedFailure(t *testing.T) {
	t.Parallel()

	failure := NewFailure(CodeRateLimited, "Slow down.", nil, nil)
	wrapped := fmt.Errorf("context: %w", failure)

	found, ok := AsFailure(wrapped)
	if !ok || found != failure {
		t.Fatalf("AsFailure() = %v, %v, want the original failure", found, ok)
	}

	if _, ok := AsFailure(errors.New("unrelated")); ok {
		t.Fatal("AsFailure() reported an unrelated error as a failure")
	}
}

func TestNewFailureCopiesDetailsSoCallersCannotMutateThem(t *testing.T) {
	t.Parallel()

	details := map[string]any{"status": 500}
	failure := NewFailure(CodeGitHubAPIError, "It failed.", details, nil)

	details["status"] = 404
	details["added"] = true

	if failure.Details["status"] != 500 {
		t.Fatalf("Details[status] = %v, want the value captured at construction", failure.Details["status"])
	}
	if _, present := failure.Details["added"]; present {
		t.Fatal("Details gained a key added after construction")
	}
}

func TestCloneDetailsKeepsNilNil(t *testing.T) {
	t.Parallel()

	if CloneDetails(nil) != nil {
		t.Fatal("CloneDetails(nil) = non-nil, want nil so the field stays omitted")
	}
}
