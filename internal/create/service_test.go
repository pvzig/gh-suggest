package create

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pvzig/gh-suggest/internal/attempt"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/suggestion"
)

const testDiff = `diff --git a/Sources/Example.swift b/Sources/Example.swift
index 1111111..2222222 100644
--- a/Sources/Example.swift
+++ b/Sources/Example.swift
@@ -39,5 +39,5 @@
 before
-old one
-old two
+new one
+new two
 after
 other
diff --git a/Sources/Other.swift b/Sources/Other.swift
index 3333333..4444444 100644
--- a/Sources/Other.swift
+++ b/Sources/Other.swift
@@ -15,3 +15,4 @@
 alpha
 beta
+added
 gamma
`

type resolverStub struct {
	ref   domain.PullRequestRef
	err   error
	calls int
}

func (stub *resolverStub) Resolve(
	context.Context,
	string,
	string,
) (domain.PullRequestRef, error) {
	stub.calls++
	return stub.ref, stub.err
}

type pullReaderStub struct {
	values []domain.PullRequest
	errs   []error
	calls  int
}

func (stub *pullReaderStub) ReadPullRequest(
	context.Context,
	domain.PullRequestRef,
) (domain.PullRequest, error) {
	index := stub.calls
	stub.calls++
	if index < len(stub.errs) && stub.errs[index] != nil {
		return domain.PullRequest{}, stub.errs[index]
	}
	if index >= len(stub.values) {
		return domain.PullRequest{}, errors.New("unexpected metadata read")
	}
	return stub.values[index], nil
}

type diffReaderStub struct {
	raw   []byte
	err   error
	calls int
}

func (stub *diffReaderStub) ReadDiff(context.Context, domain.PullRequestRef) ([]byte, error) {
	stub.calls++
	return stub.raw, stub.err
}

type reviewCreatorStub struct {
	created CreatedReview
	err     error
	calls   int
	request ReviewRequest
}

func (stub *reviewCreatorStub) CreateReview(
	_ context.Context,
	request ReviewRequest,
) (CreatedReview, error) {
	stub.calls++
	stub.request = cloneReviewRequest(request)
	return stub.created, stub.err
}

func TestDryRunValidatesOrderedReviewWithoutWrite(t *testing.T) {
	t.Parallel()

	service, resolver, pulls, diffs, reviews := serviceParts()
	request := validDryRunRequest()

	result, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Posted || result.ReviewID != 0 || result.URL != "" {
		t.Fatalf("result = %#v, want validated non-posted review", result)
	}
	if reviews.calls != 0 {
		t.Fatalf("CreateReview calls = %d, want 0", reviews.calls)
	}
	if resolver.calls != 1 || pulls.calls != 2 || diffs.calls != 1 {
		t.Fatalf(
			"read calls = resolver:%d pulls:%d diffs:%d, want 1, 2, 1",
			resolver.calls,
			pulls.calls,
			diffs.calls,
		)
	}
	if result.RequestSHA256 == "" ||
		result.HeadSHA != testHeadSHA ||
		result.ReviewBodySHA256 != suggestion.BodySHA256("Two focused fixes.\nPlease review.") {
		t.Fatalf("digest result = %#v", result)
	}
	if len(result.Suggestions) != 2 {
		t.Fatalf("suggestion summaries = %d, want 2", len(result.Suggestions))
	}
	if result.Suggestions[0].Path != "Sources/Example.swift" ||
		result.Suggestions[1].Path != "Sources/Other.swift" {
		t.Fatalf("ordered summaries = %#v", result.Suggestions)
	}
	first := result.Suggestions[0]
	if first.StartLine == nil || *first.StartLine != 40 || first.EndLine != 41 {
		t.Fatalf("first range = %#v", first)
	}
	if first.Replacement.ByteCount != len("replacement") ||
		first.Replacement.LineCount != 1 ||
		first.Replacement.SHA256 != suggestion.BodySHA256("replacement") {
		t.Fatalf("first replacement metadata = %#v", first.Replacement)
	}
	wantFirstBody := "Keep cancellation.\n\n```suggestion\nreplacement\n```"
	if first.BodySHA256 != suggestion.BodySHA256(wantFirstBody) {
		t.Fatalf("first body digest = %q", first.BodySHA256)
	}
}

func TestDirectWriteValidatesAndPostsOneExactReviewPayload(t *testing.T) {
	t.Parallel()

	service, _, pulls, diffs, reviews := serviceParts()
	request := validWriteRequest()

	result, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Posted || result.ReviewID != 456 || result.URL == "" {
		t.Fatalf("result = %#v, want created review", result)
	}
	if reviews.calls != 1 || pulls.calls != 2 || diffs.calls != 1 {
		t.Fatalf(
			"calls = reviews:%d pulls:%d diffs:%d, want 1, 2, 1",
			reviews.calls,
			pulls.calls,
			diffs.calls,
		)
	}
	got := reviews.request
	if got.Ref != testPullRequestRef() ||
		got.BaseSHA != testBaseSHA ||
		got.HeadSHA != testHeadSHA ||
		got.Event != domain.ReviewEventComment ||
		got.Body != "Two focused fixes.\nPlease review." ||
		got.RequestSHA256 != result.RequestSHA256 {
		t.Fatalf("review request = %#v", got)
	}
	if len(got.Comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(got.Comments))
	}
	if got.Comments[0].Path != "Sources/Example.swift" ||
		got.Comments[0].StartLine == nil ||
		*got.Comments[0].StartLine != 40 ||
		got.Comments[0].EndLine != 41 ||
		got.Comments[0].Body != "Keep cancellation.\n\n```suggestion\nreplacement\n```" {
		t.Fatalf("first comment = %#v", got.Comments[0])
	}
	if got.Comments[1].Path != "Sources/Other.swift" ||
		got.Comments[1].StartLine != nil ||
		got.Comments[1].EndLine != 17 ||
		got.Comments[1].Body != "```suggestion\nother\n```" {
		t.Fatalf("second comment = %#v", got.Comments[1])
	}
}

func TestOptionalExpectedHeadIsAcceptedForWritesAndDryRuns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dryRun     bool
		wantPosted bool
		wantWrites int
	}{
		{name: "write", wantPosted: true, wantWrites: 1},
		{name: "dry run", dryRun: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			service, _, _, _, reviews := serviceParts()
			request := validWriteRequest()
			request.DryRun = test.dryRun
			request.ExpectedHeadSHA = strings.ToUpper(testHeadSHA)

			result, err := service.Execute(context.Background(), request)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.Posted != test.wantPosted || reviews.calls != test.wantWrites {
				t.Fatalf("result = %#v, CreateReview calls = %d", result, reviews.calls)
			}
		})
	}
}

func TestDeletionSuggestionRendersAnEmptyBlockInsideReview(t *testing.T) {
	t.Parallel()

	request := validWriteRequest()
	request.Suggestions[1].Replacement = nil
	service, _, _, _, reviews := serviceParts()
	if _, err := service.Execute(context.Background(), request); err != nil {
		t.Fatalf("write Execute() error = %v", err)
	}
	if reviews.request.Comments[1].Body != "```suggestion\n```" {
		t.Errorf("deletion body = %q", reviews.request.Comments[1].Body)
	}
}

func TestIndefiniteCreatedReviewReportsFileSafeReconciliationDescriptor(t *testing.T) {
	t.Parallel()

	service, _, _, _, reviews := serviceParts()
	reviews.created = CreatedReview{}
	service.now = func() time.Time {
		return time.Date(2026, 7, 27, 12, 34, 56, 789, time.FixedZone("offset", 3600))
	}

	_, err := service.Execute(context.Background(), validWriteRequest())
	failure := assertFailureCode(t, err, domain.CodeWriteOutcomeUnknown)

	descriptor, ok := failure.Details["reconciliation"].(attempt.Descriptor)
	if !ok {
		t.Fatalf("reconciliation = %#v", failure.Details["reconciliation"])
	}
	if descriptor.SchemaVersion != 1 ||
		descriptor.Host != "github.com" ||
		descriptor.Repository != "owner/repo" ||
		descriptor.PullRequest != 123 ||
		descriptor.BaseSHA != testBaseSHA ||
		descriptor.HeadSHA != testHeadSHA ||
		descriptor.Event != domain.ReviewEventComment ||
		descriptor.ReviewBodySHA256 != suggestion.BodySHA256(reviews.request.Body) ||
		descriptor.RequestSHA256 != reviews.request.RequestSHA256 ||
		descriptor.AttemptStartedAt != time.Date(2026, 7, 27, 11, 34, 56, 789, time.UTC) {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	if len(descriptor.Suggestions) != 2 {
		t.Fatalf("descriptor suggestions = %d, want 2", len(descriptor.Suggestions))
	}
	for index, item := range descriptor.Suggestions {
		comment := reviews.request.Comments[index]
		if item.Path != comment.Path ||
			item.EndLine != comment.EndLine ||
			item.Side != string(domain.SideRight) ||
			item.BodySHA256 != suggestion.BodySHA256(comment.Body) {
			t.Errorf("descriptor suggestion %d = %#v", index, item)
		}
	}

	encoded, marshalErr := json.Marshal(failure.Details)
	if marshalErr != nil {
		t.Fatalf("Marshal(details) error = %v", marshalErr)
	}
	for _, secret := range []string{
		"Two focused fixes.",
		"Keep cancellation.",
		"replacement",
		"```suggestion",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("reconciliation details expose %q: %s", secret, encoded)
		}
	}
	guidance, _ := failure.Details["guidance"].(string)
	if !strings.Contains(guidance, "--attempt-file FILE") ||
		!strings.Contains(guidance, "do not retry") {
		t.Errorf("guidance = %q", guidance)
	}
}

func TestNoWriteOnExpectedSnapshotAndRangeFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*Request, *pullReaderStub, *diffReaderStub)
		wantCode  domain.Code
		wantReads int
		wantDiffs int
		wantIndex any
	}{
		{
			name: "optional expected head is stale",
			mutate: func(request *Request, _ *pullReaderStub, _ *diffReaderStub) {
				request.ExpectedHeadSHA = alternateHeadSHA
			},
			wantCode:  domain.CodeStalePullRequest,
			wantReads: 1,
		},
		{
			name: "head changes before final read",
			mutate: func(_ *Request, pulls *pullReaderStub, _ *diffReaderStub) {
				pulls.values[1].HeadSHA = alternateHeadSHA
			},
			wantCode:  domain.CodeStalePullRequest,
			wantReads: 2,
			wantDiffs: 1,
		},
		{
			name: "base changes before final read",
			mutate: func(_ *Request, pulls *pullReaderStub, _ *diffReaderStub) {
				pulls.values[1].BaseSHA = alternateBaseSHA
			},
			wantCode:  domain.CodeStalePullRequest,
			wantReads: 2,
			wantDiffs: 1,
		},
		{
			name: "head advances before diff fetch and invalidates range",
			mutate: func(request *Request, pulls *pullReaderStub, _ *diffReaderStub) {
				request.Suggestions[1].EndLine = 100
				pulls.values[1].HeadSHA = alternateHeadSHA
			},
			wantCode:  domain.CodeStalePullRequest,
			wantReads: 2,
			wantDiffs: 1,
		},
		{
			name: "second range is outside hunk",
			mutate: func(request *Request, _ *pullReaderStub, _ *diffReaderStub) {
				request.Suggestions[1].EndLine = 100
			},
			wantCode:  domain.CodeRangeNotCommentable,
			wantReads: 2,
			wantDiffs: 1,
			wantIndex: 1,
		},
		{
			name: "diff file count is incomplete",
			mutate: func(request *Request, pulls *pullReaderStub, _ *diffReaderStub) {
				pulls.values[0].ChangedFiles = 3
			},
			wantCode:  domain.CodeRangeNotCommentable,
			wantReads: 2,
			wantDiffs: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			service, resolver, pulls, diffs, reviews := serviceParts()
			request := validWriteRequest()
			test.mutate(&request, pulls, diffs)

			_, err := service.Execute(context.Background(), request)
			failure := assertFailureCode(t, err, test.wantCode)
			if reviews.calls != 0 {
				t.Fatalf("CreateReview calls = %d, want 0", reviews.calls)
			}
			if resolver.calls != 1 ||
				pulls.calls != test.wantReads ||
				diffs.calls != test.wantDiffs {
				t.Fatalf(
					"calls = resolver:%d pulls:%d diffs:%d, want 1, %d, %d",
					resolver.calls,
					pulls.calls,
					diffs.calls,
					test.wantReads,
					test.wantDiffs,
				)
			}
			if test.wantIndex != nil && failure.Details["suggestionIndex"] != test.wantIndex {
				t.Errorf("suggestionIndex = %#v, want %#v", failure.Details["suggestionIndex"], test.wantIndex)
			}
		})
	}
}

func TestLocalValidationRejectsInvalidGroupedReviews(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*Request)
		detailKey  string
		detailWant any
	}{
		{
			name: "no suggestions",
			mutate: func(request *Request) {
				request.Suggestions = nil
			},
			detailKey:  "suggestionCount",
			detailWant: 0,
		},
		{
			name: "too many suggestions",
			mutate: func(request *Request) {
				request.Suggestions = make([]Suggestion, MaxSuggestions+1)
			},
			detailKey:  "suggestionCount",
			detailWant: MaxSuggestions + 1,
		},
		{
			name: "empty review body",
			mutate: func(request *Request) {
				request.ReviewBody = " \r\n\t"
			},
			detailKey:  "field",
			detailWant: "reviewBody",
		},
		{
			name: "invalid review body UTF-8",
			mutate: func(request *Request) {
				request.ReviewBody = string([]byte{0xff})
			},
			detailKey:  "field",
			detailWant: "reviewBody",
		},
		{
			name: "reconciliation-unstable review body",
			mutate: func(request *Request) {
				request.ReviewBody = "Review\x1b body"
			},
			detailKey:  "field",
			detailWant: "reviewBody",
		},
		{
			name: "unclean path",
			mutate: func(request *Request) {
				request.Suggestions[1].Path = "../secret"
			},
			detailKey:  "suggestionIndex",
			detailWant: 1,
		},
		{
			name: "nonpositive end line",
			mutate: func(request *Request) {
				request.Suggestions[0].EndLine = 0
			},
			detailKey:  "suggestionIndex",
			detailWant: 0,
		},
		{
			name: "start equals end",
			mutate: func(request *Request) {
				line := request.Suggestions[0].EndLine
				request.Suggestions[0].StartLine = &line
			},
			detailKey:  "suggestionIndex",
			detailWant: 0,
		},
		{
			name: "invalid replacement UTF-8",
			mutate: func(request *Request) {
				request.Suggestions[1].Replacement = []byte{0xff}
			},
			detailKey:  "suggestionIndex",
			detailWant: 1,
		},
		{
			name: "reconciliation-unstable replacement",
			mutate: func(request *Request) {
				request.Suggestions[1].Replacement = []byte("value\x1b")
			},
			detailKey:  "suggestionIndex",
			detailWant: 1,
		},
		{
			name: "invalid note fence",
			mutate: func(request *Request) {
				request.Suggestions[1].Note = "```\nopen"
			},
			detailKey:  "suggestionIndex",
			detailWant: 1,
		},
		{
			name: "invalid note raw HTML block",
			mutate: func(request *Request) {
				request.Suggestions[1].Note = "<!-- open"
			},
			detailKey:  "suggestionIndex",
			detailWant: 1,
		},
		{
			name: "abbreviated optional head",
			mutate: func(request *Request) {
				request.ExpectedHeadSHA = testHeadSHA[:8]
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := validDryRunRequest()
			test.mutate(&request)
			failure := ValidateLocalRequest(request)
			if failure == nil || failure.Code != domain.CodeInvalidArguments {
				t.Fatalf("ValidateLocalRequest() = %#v", failure)
			}
			if test.detailKey != "" &&
				failure.Details[test.detailKey] != test.detailWant {
				t.Errorf(
					"details[%s] = %#v, want %#v",
					test.detailKey,
					failure.Details[test.detailKey],
					test.detailWant,
				)
			}
		})
	}
}

func TestValidateLocalRequestAcceptsMaximumSuggestionCount(t *testing.T) {
	t.Parallel()

	request := validDryRunRequest()
	request.Suggestions = make([]Suggestion, MaxSuggestions)
	for index := range request.Suggestions {
		request.Suggestions[index] = Suggestion{
			Path:        "Sources/File" + strconv.Itoa(index) + ".swift",
			EndLine:     index + 1,
			Replacement: []byte("replacement"),
		}
	}

	if failure := ValidateLocalRequest(request); failure != nil {
		t.Fatalf("ValidateLocalRequest() failure = %v", failure)
	}
}

func TestLocalValidationRejectsOverlappingRangesOnlyWithinSamePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		suggestions []Suggestion
		wantFailure bool
	}{
		{
			name: "identical single line",
			suggestions: []Suggestion{
				{Path: "file.go", EndLine: 10},
				{Path: "file.go", EndLine: 10},
			},
			wantFailure: true,
		},
		{
			name: "multiline intersection",
			suggestions: func() []Suggestion {
				firstStart := 8
				secondStart := 10
				return []Suggestion{
					{Path: "file.go", StartLine: &firstStart, EndLine: 10},
					{Path: "file.go", StartLine: &secondStart, EndLine: 12},
				}
			}(),
			wantFailure: true,
		},
		{
			name: "adjacent ranges",
			suggestions: func() []Suggestion {
				firstStart := 8
				secondStart := 11
				return []Suggestion{
					{Path: "file.go", StartLine: &firstStart, EndLine: 10},
					{Path: "file.go", StartLine: &secondStart, EndLine: 12},
				}
			}(),
		},
		{
			name: "same lines in different files",
			suggestions: []Suggestion{
				{Path: "first.go", EndLine: 10},
				{Path: "second.go", EndLine: 10},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			for index := range test.suggestions {
				test.suggestions[index].Replacement = []byte("value")
			}
			request := Request{
				ReviewBody:  "Review summary.",
				Suggestions: test.suggestions,
				DryRun:      true,
			}
			failure := ValidateLocalRequest(request)
			if test.wantFailure {
				if failure == nil || failure.Code != domain.CodeInvalidArguments {
					t.Fatalf("ValidateLocalRequest() = %#v, want invalid_arguments", failure)
				}
				if failure.Details["firstIndex"] != 0 ||
					failure.Details["secondIndex"] != 1 {
					t.Errorf("overlap details = %#v", failure.Details)
				}
				return
			}
			if failure != nil {
				t.Fatalf("ValidateLocalRequest() = %v", failure)
			}
		})
	}
}

func TestLocalValidationEnforcesAggregateReplacementAndProseLimits(t *testing.T) {
	t.Parallel()

	t.Run("replacement over limit", func(t *testing.T) {
		t.Parallel()

		request := validDryRunRequest()
		request.Suggestions[0].Replacement = []byte(
			strings.Repeat("a", (maxAggregateReplacementBytes/2)+1),
		)
		request.Suggestions[1].Replacement = []byte(
			strings.Repeat("b", maxAggregateReplacementBytes/2),
		)
		failure := ValidateLocalRequest(request)
		if failure == nil ||
			failure.Code != domain.CodeInvalidArguments ||
			failure.Details["maximumBytes"] != maxAggregateReplacementBytes {
			t.Fatalf("ValidateLocalRequest() = %#v", failure)
		}
	})

	t.Run("prose over limit", func(t *testing.T) {
		t.Parallel()

		request := validDryRunRequest()
		request.ReviewBody = strings.Repeat("a", maxAggregateProseBytes/2)
		request.Suggestions[0].Note = strings.Repeat("b", (maxAggregateProseBytes/4)+1)
		request.Suggestions[1].Note = strings.Repeat("c", maxAggregateProseBytes/4)
		failure := ValidateLocalRequest(request)
		if failure == nil ||
			failure.Code != domain.CodeInvalidArguments ||
			failure.Details["maximumBytes"] != maxAggregateProseBytes {
			t.Fatalf("ValidateLocalRequest() = %#v", failure)
		}
	})

	t.Run("inclusive aggregate limits", func(t *testing.T) {
		t.Parallel()

		request := validDryRunRequest()
		request.Suggestions[0].Replacement = []byte(
			strings.Repeat("a", maxAggregateReplacementBytes/2),
		)
		request.Suggestions[1].Replacement = []byte(
			strings.Repeat("b", maxAggregateReplacementBytes/2),
		)
		request.ReviewBody = strings.Repeat("a", maxAggregateProseBytes/2)
		request.Suggestions[0].Note = strings.Repeat("b", maxAggregateProseBytes/4)
		request.Suggestions[1].Note = strings.Repeat("c", maxAggregateProseBytes/4)
		if failure := ValidateLocalRequest(request); failure != nil {
			t.Fatalf("ValidateLocalRequest() = %v", failure)
		}
	})
}

func TestValidateRequestFieldsDoesNotInspectReplacementBytes(t *testing.T) {
	t.Parallel()

	request := validDryRunRequest()
	request.Suggestions[0].Replacement = []byte{0xff}
	if failure := ValidateRequestFields(request); failure != nil {
		t.Fatalf("ValidateRequestFields() = %v", failure)
	}
	failure := ValidateLocalRequest(request)
	if failure == nil ||
		failure.Code != domain.CodeInvalidArguments ||
		failure.Details["suggestionIndex"] != 0 {
		t.Fatalf("ValidateLocalRequest() = %#v", failure)
	}
}

func TestInvalidRequestStopsBeforeExternalReads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{
			name: "missing review body",
			mutate: func(request *Request) {
				request.ReviewBody = ""
			},
		},
		{
			name: "overlapping ranges",
			mutate: func(request *Request) {
				request.Suggestions[1].Path = request.Suggestions[0].Path
				request.Suggestions[1].EndLine = request.Suggestions[0].EndLine
			},
		},
		{
			name: "aggregate replacement overflow",
			mutate: func(request *Request) {
				request.Suggestions[0].Replacement = []byte(
					strings.Repeat("a", maxAggregateReplacementBytes),
				)
				request.Suggestions[1].Replacement = []byte("b")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			service, resolver, pulls, diffs, reviews := serviceParts()
			request := validDryRunRequest()
			test.mutate(&request)

			_, err := service.Execute(context.Background(), request)
			_ = assertFailureCode(t, err, domain.CodeInvalidArguments)
			if resolver.calls != 0 || pulls.calls != 0 || diffs.calls != 0 || reviews.calls != 0 {
				t.Fatalf(
					"external calls = resolver:%d pulls:%d diffs:%d reviews:%d",
					resolver.calls,
					pulls.calls,
					diffs.calls,
					reviews.calls,
				)
			}
		})
	}
}

func TestReviewCreatorStableFailureIsPreserved(t *testing.T) {
	t.Parallel()

	service, _, _, _, reviews := serviceParts()
	reviews.err = domain.NewFailure(
		domain.CodePermissionDenied,
		"permission denied",
		map[string]any{"scope": "pull_requests"},
		nil,
	)

	_, err := service.Execute(context.Background(), validWriteRequest())
	failure := assertFailureCode(t, err, domain.CodePermissionDenied)
	if failure.Details["scope"] != "pull_requests" || reviews.calls != 1 {
		t.Fatalf("failure = %#v, review calls = %d", failure, reviews.calls)
	}
}

const (
	testBaseSHA      = "1111111111111111111111111111111111111111"
	testHeadSHA      = "2222222222222222222222222222222222222222"
	alternateBaseSHA = "3333333333333333333333333333333333333333"
	alternateHeadSHA = "4444444444444444444444444444444444444444"
)

func testPullRequestRef() domain.PullRequestRef {
	return domain.PullRequestRef{
		Repository: domain.Repository{Host: "github.com", Owner: "owner", Name: "repo"},
		Number:     123,
	}
}

func serviceParts() (
	*Service,
	*resolverStub,
	*pullReaderStub,
	*diffReaderStub,
	*reviewCreatorStub,
) {
	ref := testPullRequestRef()
	metadata := domain.PullRequest{
		Ref:          ref,
		URL:          "https://github.com/owner/repo/pull/123",
		State:        domain.StateOpen,
		BaseSHA:      testBaseSHA,
		HeadSHA:      testHeadSHA,
		ChangedFiles: 2,
	}
	resolver := &resolverStub{ref: ref}
	pulls := &pullReaderStub{values: []domain.PullRequest{metadata, metadata}}
	diffs := &diffReaderStub{raw: []byte(testDiff)}
	reviews := &reviewCreatorStub{
		created: CreatedReview{
			ID:  456,
			URL: "https://github.com/owner/repo/pull/123#pullrequestreview-456",
		},
	}
	return NewService(resolver, pulls, diffs, reviews), resolver, pulls, diffs, reviews
}

func validDryRunRequest() Request {
	startLine := 40
	return Request{
		Selector:   "123",
		Repository: "owner/repo",
		ReviewBody: "Two focused fixes.\r\nPlease review.",
		Suggestions: []Suggestion{
			{
				Path:        "Sources/Example.swift",
				StartLine:   &startLine,
				EndLine:     41,
				Replacement: []byte("replacement\n"),
				Note:        "Keep cancellation.",
			},
			{
				Path:        "Sources/Other.swift",
				EndLine:     17,
				Replacement: []byte("other\r\n"),
			},
		},
		DryRun: true,
	}
}

func validWriteRequest() Request {
	request := validDryRunRequest()
	request.DryRun = false
	return request
}

func assertFailureCode(t *testing.T, err error, code domain.Code) *domain.Failure {
	t.Helper()
	if err == nil {
		t.Fatal("Execute() error = nil")
	}
	failure, ok := domain.AsFailure(err)
	if !ok {
		t.Fatalf("error type = %T, want *domain.Failure: %v", err, err)
	}
	if failure.Code != code {
		t.Fatalf("failure code = %q, want %q: %v", failure.Code, code, failure)
	}
	return failure
}

func cloneReviewRequest(request ReviewRequest) ReviewRequest {
	cloned := request
	cloned.Comments = make([]ReviewComment, len(request.Comments))
	copy(cloned.Comments, request.Comments)
	for index := range cloned.Comments {
		cloned.Comments[index].StartLine = optionalLineCopy(request.Comments[index].StartLine)
	}
	return cloned
}
