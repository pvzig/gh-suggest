package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pvzig/gh-suggest/internal/attempt"
	"github.com/pvzig/gh-suggest/internal/domain"
)

const (
	testBaseSHA       = "1111111111111111111111111111111111111111"
	testHeadSHA       = "2222222222222222222222222222222222222222"
	otherHeadSHA      = "3333333333333333333333333333333333333333"
	testRequestSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testReviewBody    = "Suggested changes.\n\n" +
		"<!-- gh-suggest:request-sha256=" + testRequestSHA256 + "; guard=v2 -->"
	firstCommentBody  = "Keep cancellation.\n\n```suggestion\nreplacement\n```"
	secondCommentBody = "Avoid the force unwrap.\n\n```suggestion\nreturn value\n```"
)

var testAttempt = time.Date(2026, time.July, 27, 12, 34, 56, 789, time.UTC)

type reviewListerStub struct {
	reviews   []Review
	err       error
	calls     int
	ref       domain.PullRequestRef
	notBefore time.Time
}

func (stub *reviewListerStub) ListReviews(
	_ context.Context,
	ref domain.PullRequestRef,
	notBefore time.Time,
) ([]Review, error) {
	stub.calls++
	stub.ref = ref
	stub.notBefore = notBefore
	return stub.reviews, stub.err
}

type reviewCommentListerStub struct {
	commentsByReview map[int64][]ReviewComment
	errorsByReview   map[int64]error
	calls            []int64
	refs             []domain.PullRequestRef
}

func (stub *reviewCommentListerStub) ListReviewComments(
	_ context.Context,
	ref domain.PullRequestRef,
	reviewID int64,
) ([]ReviewComment, error) {
	stub.calls = append(stub.calls, reviewID)
	stub.refs = append(stub.refs, ref)
	return stub.commentsByReview[reviewID], stub.errorsByReview[reviewID]
}

var (
	_ ReviewLister        = (*reviewListerStub)(nil)
	_ ReviewCommentLister = (*reviewCommentListerStub)(nil)
)

func TestValidateLocalRequestRejectsInvalidDescriptorsBeforeReads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(*attempt.Descriptor)
		wantCode domain.Code
	}{
		{
			name:   "schema version",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.SchemaVersion++ },
		},
		{
			name:     "unsupported host",
			mutate:   func(descriptor *attempt.Descriptor) { descriptor.Host = "example.com" },
			wantCode: domain.CodeUnsupportedHost,
		},
		{
			name:   "repository missing owner",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.Repository = "repo" },
		},
		{
			name:   "repository has extra component",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.Repository = "owner/group/repo" },
		},
		{
			name: "repository owner",
			mutate: func(descriptor *attempt.Descriptor) {
				descriptor.Repository = "own!er/repository"
			},
		},
		{
			name: "repository name",
			mutate: func(descriptor *attempt.Descriptor) {
				descriptor.Repository = "owner/repo?query"
			},
		},
		{
			name:   "pull request",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.PullRequest = 0 },
		},
		{
			name:   "base SHA",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.BaseSHA = testBaseSHA[:8] },
		},
		{
			name:   "head SHA",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.HeadSHA = testHeadSHA[:8] },
		},
		{
			name:   "event",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.Event = "APPROVE" },
		},
		{
			name:   "review body digest",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.ReviewBodySHA256 = "bad" },
		},
		{
			name:   "request digest",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.RequestSHA256 = "bad" },
		},
		{
			name:   "attempt timestamp",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.AttemptStartedAt = time.Time{} },
		},
		{
			name:   "empty suggestions",
			mutate: func(descriptor *attempt.Descriptor) { descriptor.Suggestions = nil },
		},
		{
			name: "too many suggestions",
			mutate: func(descriptor *attempt.Descriptor) {
				descriptor.Suggestions = make(
					[]attempt.Suggestion,
					domain.MaxSuggestions+1,
				)
			},
		},
		{
			name: "suggestion path",
			mutate: func(descriptor *attempt.Descriptor) {
				descriptor.Suggestions[0].Path = "../secret"
			},
		},
		{
			name: "suggestion end line",
			mutate: func(descriptor *attempt.Descriptor) {
				descriptor.Suggestions[0].EndLine = 0
			},
		},
		{
			name: "suggestion start line",
			mutate: func(descriptor *attempt.Descriptor) {
				line := 0
				descriptor.Suggestions[0].StartLine = &line
			},
		},
		{
			name: "suggestion equal range",
			mutate: func(descriptor *attempt.Descriptor) {
				line := descriptor.Suggestions[0].EndLine
				descriptor.Suggestions[0].StartLine = &line
			},
		},
		{
			name: "suggestion reversed range",
			mutate: func(descriptor *attempt.Descriptor) {
				line := descriptor.Suggestions[0].EndLine + 1
				descriptor.Suggestions[0].StartLine = &line
			},
		},
		{
			name: "suggestion side",
			mutate: func(descriptor *attempt.Descriptor) {
				descriptor.Suggestions[0].Side = "LEFT"
			},
		},
		{
			name: "suggestion body digest",
			mutate: func(descriptor *attempt.Descriptor) {
				descriptor.Suggestions[0].BodySHA256 = "bad"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			descriptor := validDescriptor()
			test.mutate(&descriptor)
			wantCode := test.wantCode
			if wantCode == "" {
				wantCode = domain.CodeInvalidArguments
			}

			failure := ValidateLocalRequest(Request{Descriptor: descriptor})
			if failure == nil || failure.Code != wantCode {
				t.Fatalf("ValidateLocalRequest() = %v, want %s", failure, wantCode)
			}

			reviews := &reviewListerStub{}
			comments := &reviewCommentListerStub{}
			_, err := NewService(reviews, comments).Execute(
				context.Background(),
				Request{Descriptor: descriptor},
			)
			assertFailureCode(t, err, wantCode)
			if reviews.calls != 0 || len(comments.calls) != 0 {
				t.Fatalf(
					"read calls = reviews:%d comments:%d, want 0/0",
					reviews.calls,
					len(comments.calls),
				)
			}
		})
	}
}

func TestValidateLocalRequestAcceptsMaximumSuggestionCount(t *testing.T) {
	t.Parallel()

	descriptor := validDescriptor()
	validSuggestion := descriptor.Suggestions[0]
	descriptor.Suggestions = make([]attempt.Suggestion, domain.MaxSuggestions)
	for index := range descriptor.Suggestions {
		descriptor.Suggestions[index] = validSuggestion
	}

	if failure := ValidateLocalRequest(Request{Descriptor: descriptor}); failure != nil {
		t.Fatalf("ValidateLocalRequest() = %v", failure)
	}
}

func TestExecuteNormalizesSupportedHostCase(t *testing.T) {
	t.Parallel()

	descriptor := validDescriptor()
	descriptor.Host = "GITHUB.COM"
	reviews := &reviewListerStub{}
	result, err := NewService(reviews, &reviewCommentListerStub{}).Execute(
		context.Background(),
		Request{Descriptor: descriptor},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Host != domain.SupportedHost ||
		reviews.ref.Repository.Host != domain.SupportedHost {
		t.Fatalf("result/ref hosts = %q/%q", result.Host, reviews.ref.Repository.Host)
	}
}

func TestExecuteReportsOneExactWholeReviewMatch(t *testing.T) {
	t.Parallel()

	descriptor := validDescriptor()
	irrelevant := exactReview(descriptor, 111)
	irrelevant.Body = "A different review."
	exact := exactReview(descriptor, 456)
	exact.State = strings.ToLower(domain.ReviewStateCommented)
	reviews := &reviewListerStub{reviews: []Review{irrelevant, exact}}
	comments := &reviewCommentListerStub{
		commentsByReview: map[int64][]ReviewComment{
			exact.ID: exactComments(descriptor),
		},
	}

	result, err := NewService(reviews, comments).Execute(
		context.Background(),
		Request{Descriptor: descriptor},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != domain.ReconciliationLikelyCreated ||
		result.MatchCount != 1 ||
		result.CandidateCount != 1 ||
		result.PartialMatchCount != 0 ||
		result.ReviewID != exact.ID ||
		result.URL != exact.URL {
		t.Fatalf("result = %#v, want one exact review", result)
	}
	if result.Host != descriptor.Host ||
		result.Repository != descriptor.Repository ||
		result.PullRequest != descriptor.PullRequest ||
		result.HeadSHA != descriptor.HeadSHA ||
		result.RequestSHA256 != descriptor.RequestSHA256 ||
		!result.AttemptStartedAt.Equal(descriptor.AttemptStartedAt) ||
		result.SuggestionCount != len(descriptor.Suggestions) {
		t.Errorf("result descriptor projection = %#v", result)
	}
	if reviews.calls != 1 ||
		!reviews.notBefore.Equal(testAttempt.Add(-ClockSkewWindow)) ||
		reviews.ref.Repository.String() != descriptor.Repository ||
		reviews.ref.Number != descriptor.PullRequest {
		t.Errorf("review read = %#v", reviews)
	}
	if len(comments.calls) != 1 || comments.calls[0] != exact.ID {
		t.Fatalf("comment reads = %v, want [%d]", comments.calls, exact.ID)
	}
	if len(comments.refs) != 1 || comments.refs[0] != reviews.ref {
		t.Errorf("comment ref = %#v, want %#v", comments.refs, reviews.ref)
	}
}

func TestExecuteMatchesCommentsWithoutDependingOnListOrder(t *testing.T) {
	t.Parallel()

	descriptor := validDescriptor()
	review := exactReview(descriptor, 456)
	expected := exactComments(descriptor)
	reversed := []ReviewComment{expected[1], expected[0]}
	reviews := &reviewListerStub{reviews: []Review{review}}
	comments := &reviewCommentListerStub{
		commentsByReview: map[int64][]ReviewComment{review.ID: reversed},
	}

	result, err := NewService(reviews, comments).Execute(
		context.Background(),
		Request{Descriptor: descriptor},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != domain.ReconciliationLikelyCreated || result.MatchCount != 1 {
		t.Fatalf("result = %#v, want order-independent exact match", result)
	}
}

func TestExecutePreservesDuplicateSuggestionCardinality(t *testing.T) {
	t.Parallel()

	descriptor := validDescriptor()
	descriptor.Suggestions = append(
		descriptor.Suggestions,
		descriptor.Suggestions[0],
	)
	review := exactReview(descriptor, 456)
	expected := exactComments(descriptor)
	comments := []ReviewComment{expected[0], expected[1], expected[0]}
	reviews := &reviewListerStub{reviews: []Review{review}}
	commentLister := &reviewCommentListerStub{
		commentsByReview: map[int64][]ReviewComment{review.ID: comments},
	}

	result, err := NewService(reviews, commentLister).Execute(
		context.Background(),
		Request{Descriptor: descriptor},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != domain.ReconciliationLikelyCreated ||
		result.MatchCount != 1 ||
		result.SuggestionCount != 3 {
		t.Fatalf("result = %#v, want one match with duplicate multiplicity", result)
	}
}

func TestExecuteLeavesZeroPartialMultipleAndCardinalityMismatchesUnknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		configure        func(attempt.Descriptor) ([]Review, map[int64][]ReviewComment)
		wantCandidates   int
		wantPartial      int
		wantMatches      int
		wantCommentReads int
	}{
		{
			name: "zero metadata candidates",
			configure: func(descriptor attempt.Descriptor) ([]Review, map[int64][]ReviewComment) {
				review := exactReview(descriptor, 101)
				review.Body = "different"
				return []Review{review}, nil
			},
		},
		{
			name: "partial same-cardinality review",
			configure: func(descriptor attempt.Descriptor) ([]Review, map[int64][]ReviewComment) {
				review := exactReview(descriptor, 102)
				comments := exactComments(descriptor)
				comments[1].Body = "different"
				return []Review{review}, map[int64][]ReviewComment{review.ID: comments}
			},
			wantCandidates:   1,
			wantPartial:      1,
			wantCommentReads: 1,
		},
		{
			name: "missing comment",
			configure: func(descriptor attempt.Descriptor) ([]Review, map[int64][]ReviewComment) {
				review := exactReview(descriptor, 103)
				comments := exactComments(descriptor)
				return []Review{review}, map[int64][]ReviewComment{
					review.ID: comments[:len(comments)-1],
				}
			},
			wantCandidates:   1,
			wantPartial:      1,
			wantCommentReads: 1,
		},
		{
			name: "extra comment",
			configure: func(descriptor attempt.Descriptor) ([]Review, map[int64][]ReviewComment) {
				review := exactReview(descriptor, 104)
				comments := exactComments(descriptor)
				comments = append(comments, comments[0])
				return []Review{review}, map[int64][]ReviewComment{review.ID: comments}
			},
			wantCandidates:   1,
			wantPartial:      1,
			wantCommentReads: 1,
		},
		{
			name: "multiple exact reviews",
			configure: func(descriptor attempt.Descriptor) ([]Review, map[int64][]ReviewComment) {
				first := exactReview(descriptor, 105)
				second := exactReview(descriptor, 106)
				return []Review{first, second}, map[int64][]ReviewComment{
					first.ID:  exactComments(descriptor),
					second.ID: exactComments(descriptor),
				}
			},
			wantCandidates:   2,
			wantMatches:      2,
			wantCommentReads: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			descriptor := validDescriptor()
			reviewValues, commentValues := test.configure(descriptor)
			reviews := &reviewListerStub{reviews: reviewValues}
			comments := &reviewCommentListerStub{commentsByReview: commentValues}
			result, err := NewService(reviews, comments).Execute(
				context.Background(),
				Request{Descriptor: descriptor},
			)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.Status != domain.ReconciliationUnknown ||
				result.CandidateCount != test.wantCandidates ||
				result.PartialMatchCount != test.wantPartial ||
				result.MatchCount != test.wantMatches ||
				result.ReviewID != 0 ||
				result.URL != "" {
				t.Fatalf("result = %#v", result)
			}
			if len(comments.calls) != test.wantCommentReads {
				t.Errorf(
					"comment reads = %d, want %d",
					len(comments.calls),
					test.wantCommentReads,
				)
			}
		})
	}
}

func TestExecuteRequiresEveryCommentDescriptorField(t *testing.T) {
	t.Parallel()

	otherStartLine := 41
	tests := []struct {
		name   string
		mutate func(*ReviewComment)
	}{
		{name: "body", mutate: func(comment *ReviewComment) { comment.Body += " changed" }},
		{name: "commit", mutate: func(comment *ReviewComment) { comment.CommitSHA = otherHeadSHA }},
		{name: "path", mutate: func(comment *ReviewComment) { comment.Path = "Sources/Other.swift" }},
		{name: "start line", mutate: func(comment *ReviewComment) { comment.StartLine = &otherStartLine }},
		{name: "missing start line", mutate: func(comment *ReviewComment) { comment.StartLine = nil }},
		{name: "start side", mutate: func(comment *ReviewComment) { comment.StartSide = "LEFT" }},
		{name: "end line", mutate: func(comment *ReviewComment) { comment.EndLine++ }},
		{name: "side", mutate: func(comment *ReviewComment) { comment.Side = "LEFT" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			descriptor := validDescriptor()
			review := exactReview(descriptor, 456)
			commentValues := exactComments(descriptor)
			test.mutate(&commentValues[0])
			reviews := &reviewListerStub{reviews: []Review{review}}
			comments := &reviewCommentListerStub{
				commentsByReview: map[int64][]ReviewComment{review.ID: commentValues},
			}

			result, err := NewService(reviews, comments).Execute(
				context.Background(),
				Request{Descriptor: descriptor},
			)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.Status != domain.ReconciliationUnknown ||
				result.CandidateCount != 1 ||
				result.PartialMatchCount != 1 ||
				result.MatchCount != 0 {
				t.Fatalf("result = %#v, want one partial candidate", result)
			}
		})
	}
}

func TestExecuteRejectsReviewMetadataMismatchesBeforeReadingComments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Review)
	}{
		{name: "missing ID", mutate: func(review *Review) { review.ID = 0 }},
		{name: "missing URL", mutate: func(review *Review) { review.URL = "" }},
		{name: "body", mutate: func(review *Review) { review.Body += " changed" }},
		{name: "commit", mutate: func(review *Review) { review.CommitSHA = otherHeadSHA }},
		{name: "state", mutate: func(review *Review) { review.State = "APPROVED" }},
		{
			name: "before time window",
			mutate: func(review *Review) {
				review.SubmittedAt = testAttempt.Add(-ClockSkewWindow - time.Nanosecond)
			},
		},
		{
			name: "after time window",
			mutate: func(review *Review) {
				review.SubmittedAt = testAttempt.Add(ClockSkewWindow + time.Nanosecond)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			descriptor := validDescriptor()
			review := exactReview(descriptor, 456)
			test.mutate(&review)
			reviews := &reviewListerStub{reviews: []Review{review}}
			comments := &reviewCommentListerStub{
				commentsByReview: map[int64][]ReviewComment{
					review.ID: exactComments(descriptor),
				},
			}

			result, err := NewService(reviews, comments).Execute(
				context.Background(),
				Request{Descriptor: descriptor},
			)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.Status != domain.ReconciliationUnknown ||
				result.CandidateCount != 0 ||
				result.MatchCount != 0 {
				t.Fatalf("result = %#v, want no metadata candidate", result)
			}
			if len(comments.calls) != 0 {
				t.Fatalf("comment reads = %v, want none", comments.calls)
			}
		})
	}
}

func TestExecuteIncludesBothClockSkewBoundaries(t *testing.T) {
	t.Parallel()

	for _, submittedAt := range []time.Time{
		testAttempt.Add(-ClockSkewWindow),
		testAttempt.Add(ClockSkewWindow),
	} {
		t.Run(submittedAt.Format(time.RFC3339Nano), func(t *testing.T) {
			t.Parallel()

			descriptor := validDescriptor()
			review := exactReview(descriptor, 456)
			review.SubmittedAt = submittedAt
			reviews := &reviewListerStub{reviews: []Review{review}}
			comments := &reviewCommentListerStub{
				commentsByReview: map[int64][]ReviewComment{
					review.ID: exactComments(descriptor),
				},
			}

			result, err := NewService(reviews, comments).Execute(
				context.Background(),
				Request{Descriptor: descriptor},
			)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.Status != domain.ReconciliationLikelyCreated {
				t.Fatalf("result = %#v, want inclusive boundary match", result)
			}
		})
	}
}

func TestExecuteMatchesCaseInsensitiveDigestsAndCommitSHAs(t *testing.T) {
	t.Parallel()

	descriptor := validDescriptor()
	descriptor.BaseSHA = strings.ToUpper(descriptor.BaseSHA)
	descriptor.HeadSHA = strings.ToUpper(descriptor.HeadSHA)
	descriptor.ReviewBodySHA256 = strings.ToUpper(descriptor.ReviewBodySHA256)
	descriptor.RequestSHA256 = strings.ToUpper(descriptor.RequestSHA256)
	for index := range descriptor.Suggestions {
		descriptor.Suggestions[index].BodySHA256 = strings.ToUpper(
			descriptor.Suggestions[index].BodySHA256,
		)
	}
	review := exactReview(descriptor, 456)
	review.CommitSHA = strings.ToLower(review.CommitSHA)
	comments := exactComments(descriptor)
	for index := range comments {
		comments[index].CommitSHA = strings.ToLower(comments[index].CommitSHA)
	}
	reviews := &reviewListerStub{reviews: []Review{review}}
	commentLister := &reviewCommentListerStub{
		commentsByReview: map[int64][]ReviewComment{review.ID: comments},
	}

	result, err := NewService(reviews, commentLister).Execute(
		context.Background(),
		Request{Descriptor: descriptor},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != domain.ReconciliationLikelyCreated ||
		result.HeadSHA != strings.ToLower(descriptor.HeadSHA) ||
		result.RequestSHA256 != strings.ToLower(descriptor.RequestSHA256) {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteUsesProjectedOriginalCommentCoordinates(t *testing.T) {
	t.Parallel()

	descriptor := validDescriptor()
	review := exactReview(descriptor, 456)
	projectedOriginals := exactComments(descriptor)
	movedCurrentValues := exactComments(descriptor)
	movedStart := 140
	movedCurrentValues[0].CommitSHA = otherHeadSHA
	movedCurrentValues[0].StartLine = &movedStart
	movedCurrentValues[0].EndLine = 142

	tests := []struct {
		name        string
		comments    []ReviewComment
		wantStatus  domain.ReconciliationStatus
		wantPartial int
	}{
		{
			name:       "adapter projects original commit and lines",
			comments:   projectedOriginals,
			wantStatus: domain.ReconciliationLikelyCreated,
		},
		{
			name:        "moved current coordinates do not match the attempt",
			comments:    movedCurrentValues,
			wantStatus:  domain.ReconciliationUnknown,
			wantPartial: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			reviews := &reviewListerStub{reviews: []Review{review}}
			comments := &reviewCommentListerStub{
				commentsByReview: map[int64][]ReviewComment{review.ID: test.comments},
			}
			result, err := NewService(reviews, comments).Execute(
				context.Background(),
				Request{Descriptor: descriptor},
			)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.Status != test.wantStatus ||
				result.PartialMatchCount != test.wantPartial {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestExecuteNormalizesReviewListerFailures(t *testing.T) {
	t.Parallel()

	stable := domain.NewFailure(
		domain.CodePermissionDenied,
		"Permission denied.",
		nil,
		nil,
	)
	tests := []struct {
		name          string
		err           error
		cancelContext bool
		wantCode      domain.Code
	}{
		{name: "stable failure", err: stable, wantCode: domain.CodePermissionDenied},
		{name: "generic failure", err: errors.New("read failed"), wantCode: domain.CodeGitHubAPIError},
		{name: "transport deadline", err: context.DeadlineExceeded, wantCode: domain.CodeGitHubAPIError},
		{name: "cancelled context", err: context.Canceled, cancelContext: true, wantCode: domain.CodeCancelled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			reviews := &reviewListerStub{err: test.err}
			comments := &reviewCommentListerStub{}
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancelContext {
				cancel()
			} else {
				defer cancel()
			}
			_, err := NewService(reviews, comments).Execute(
				ctx,
				Request{Descriptor: validDescriptor()},
			)
			assertFailureCode(t, err, test.wantCode)
			if reviews.calls != 1 || len(comments.calls) != 0 {
				t.Fatalf(
					"read calls = reviews:%d comments:%d, want 1/0",
					reviews.calls,
					len(comments.calls),
				)
			}
		})
	}
}

func TestExecuteNormalizesReviewCommentListerFailures(t *testing.T) {
	t.Parallel()

	stable := domain.NewFailure(
		domain.CodeRateLimited,
		"Rate limited.",
		nil,
		nil,
	)
	tests := []struct {
		name          string
		err           error
		cancelContext bool
		wantCode      domain.Code
	}{
		{name: "stable failure", err: stable, wantCode: domain.CodeRateLimited},
		{name: "generic failure", err: errors.New("read failed"), wantCode: domain.CodeGitHubAPIError},
		{name: "transport deadline", err: context.DeadlineExceeded, wantCode: domain.CodeGitHubAPIError},
		{name: "cancelled context", err: context.Canceled, cancelContext: true, wantCode: domain.CodeCancelled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			descriptor := validDescriptor()
			review := exactReview(descriptor, 456)
			reviews := &reviewListerStub{reviews: []Review{review}}
			comments := &reviewCommentListerStub{
				errorsByReview: map[int64]error{review.ID: test.err},
			}
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancelContext {
				cancel()
			} else {
				defer cancel()
			}
			_, err := NewService(reviews, comments).Execute(
				ctx,
				Request{Descriptor: descriptor},
			)
			assertFailureCode(t, err, test.wantCode)
			if reviews.calls != 1 ||
				len(comments.calls) != 1 ||
				comments.calls[0] != review.ID {
				t.Fatalf(
					"read calls = reviews:%d comments:%v",
					reviews.calls,
					comments.calls,
				)
			}
		})
	}
}

func TestServiceDependsOnlyOnGETShapedPorts(t *testing.T) {
	t.Parallel()

	descriptor := validDescriptor()
	review := exactReview(descriptor, 456)
	reviews := &reviewListerStub{reviews: []Review{review}}
	comments := &reviewCommentListerStub{
		commentsByReview: map[int64][]ReviewComment{
			review.ID: exactComments(descriptor),
		},
	}

	if _, err := NewService(reviews, comments).Execute(
		context.Background(),
		Request{Descriptor: descriptor},
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if reviews.calls != 1 || len(comments.calls) != 1 {
		t.Fatalf(
			"GET-shaped port calls = reviews:%d comments:%d, want 1/1",
			reviews.calls,
			len(comments.calls),
		)
	}
}

func validDescriptor() attempt.Descriptor {
	startLine := 40
	return attempt.Descriptor{
		SchemaVersion:    attempt.DescriptorSchemaVersion,
		Host:             "github.com",
		Repository:       "owner/repo",
		PullRequest:      123,
		BaseSHA:          testBaseSHA,
		HeadSHA:          testHeadSHA,
		Event:            "COMMENT",
		ReviewBodySHA256: digest(testReviewBody),
		Suggestions: []attempt.Suggestion{
			{
				Path:       "Sources/First.swift",
				StartLine:  &startLine,
				EndLine:    42,
				Side:       "RIGHT",
				BodySHA256: digest(firstCommentBody),
			},
			{
				Path:       "Sources/Second.swift",
				EndLine:    12,
				Side:       "RIGHT",
				BodySHA256: digest(secondCommentBody),
			},
		},
		RequestSHA256:    testRequestSHA256,
		AttemptStartedAt: testAttempt,
	}
}

func exactReview(descriptor attempt.Descriptor, id int64) Review {
	return Review{
		ID:          id,
		URL:         "https://github.com/owner/repo/pull/123#pullrequestreview-456",
		Body:        testReviewBody,
		CommitSHA:   descriptor.HeadSHA,
		State:       domain.ReviewStateCommented,
		SubmittedAt: descriptor.AttemptStartedAt,
	}
}

func exactComments(descriptor attempt.Descriptor) []ReviewComment {
	startLine := 40
	return []ReviewComment{
		{
			Body:      firstCommentBody,
			CommitSHA: descriptor.HeadSHA,
			Path:      "Sources/First.swift",
			StartLine: &startLine,
			StartSide: "RIGHT",
			EndLine:   42,
			Side:      "RIGHT",
		},
		{
			Body:      secondCommentBody,
			CommitSHA: descriptor.HeadSHA,
			Path:      "Sources/Second.swift",
			EndLine:   12,
			Side:      "RIGHT",
		},
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func assertFailureCode(t *testing.T, err error, code domain.Code) {
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
}
