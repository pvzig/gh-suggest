// Package reconcile implements read-only matching after an ambiguous grouped
// pull-request review creation response.
package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pvzig/gh-suggest/internal/attempt"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/suggestion"
)

const (
	// ClockSkewWindow bounds how far a review's submission time may differ from
	// the recorded attempt and still be considered the same write.
	ClockSkewWindow = 2 * time.Minute
)

// Request contains one descriptor loaded from an ambiguous create error.
type Request struct {
	Descriptor attempt.Descriptor
}

// Review is one submitted pull-request review considered during
// reconciliation.
type Review struct {
	ID          int64
	URL         string
	Body        string
	CommitSHA   string
	State       string
	SubmittedAt time.Time
}

// ReviewComment is one inline comment belonging to a candidate review.
type ReviewComment struct {
	Body      string
	CommitSHA string
	Path      string
	StartLine *int
	StartSide string
	EndLine   int
	Side      string
}

// ReviewLister returns submitted reviews no earlier than notBefore.
type ReviewLister interface {
	ListReviews(
		ctx context.Context,
		ref domain.PullRequestRef,
		notBefore time.Time,
	) ([]Review, error)
}

// ReviewCommentLister returns every inline comment belonging to one review.
type ReviewCommentLister interface {
	ListReviewComments(
		ctx context.Context,
		ref domain.PullRequestRef,
		reviewID int64,
	) ([]ReviewComment, error)
}

// Result reports what reconciliation could prove without changing state.
type Result struct {
	Status            domain.ReconciliationStatus
	Host              string
	Repository        string
	PullRequest       int
	HeadSHA           string
	RequestSHA256     string
	AttemptStartedAt  time.Time
	SuggestionCount   int
	CandidateCount    int
	PartialMatchCount int
	MatchCount        int
	ReviewID          int64
	URL               string
}

// Service performs whole-review, read-only reconciliation.
type Service struct {
	reviews  ReviewLister
	comments ReviewCommentLister
}

// NewService wires the GET-backed ports reconciliation depends on.
func NewService(reviews ReviewLister, comments ReviewCommentLister) *Service {
	return &Service{reviews: reviews, comments: comments}
}

// ValidateLocalRequest verifies the descriptor before authentication or any
// network request.
func ValidateLocalRequest(request Request) *domain.Failure {
	_, failure := validatedDescriptor(request.Descriptor)
	return failure
}

// Execute compares the full submitted review and its complete comment set.
// Zero, partial, and multiple matches all remain unknown and never authorize a
// retry.
func (service *Service) Execute(ctx context.Context, request Request) (Result, error) {
	ref, failure := validatedDescriptor(request.Descriptor)
	if failure != nil {
		return Result{}, failure
	}
	descriptor := request.Descriptor
	notBefore := descriptor.AttemptStartedAt.UTC().Add(-ClockSkewWindow)
	notAfter := descriptor.AttemptStartedAt.UTC().Add(ClockSkewWindow)

	reviews, err := service.reviews.ListReviews(ctx, ref, notBefore)
	if err != nil {
		return Result{}, domain.NormalizeFailure(
			ctx,
			err,
			domain.CodeGitHubAPIError,
			"GitHub reviews could not be read for reconciliation.",
			"Reconciliation was cancelled.",
		)
	}

	result := Result{
		Status:           domain.ReconciliationUnknown,
		Host:             domain.SupportedHost,
		Repository:       descriptor.Repository,
		PullRequest:      descriptor.PullRequest,
		HeadSHA:          strings.ToLower(descriptor.HeadSHA),
		RequestSHA256:    strings.ToLower(descriptor.RequestSHA256),
		AttemptStartedAt: descriptor.AttemptStartedAt.UTC(),
		SuggestionCount:  len(descriptor.Suggestions),
	}
	matches := make([]Review, 0, 1)
	for _, candidate := range reviews {
		if !matchingReviewMetadata(descriptor, candidate, notBefore, notAfter) {
			continue
		}
		result.CandidateCount++
		comments, err := service.comments.ListReviewComments(ctx, ref, candidate.ID)
		if err != nil {
			return Result{}, domain.NormalizeFailure(
				ctx,
				err,
				domain.CodeGitHubAPIError,
				"GitHub review comments could not be read for reconciliation.",
				"Reconciliation was cancelled.",
			)
		}
		if matchingCommentSet(descriptor, comments) {
			matches = append(matches, candidate)
			continue
		}
		result.PartialMatchCount++
	}

	result.MatchCount = len(matches)
	if len(matches) == 1 {
		result.Status = domain.ReconciliationLikelyCreated
		result.ReviewID = matches[0].ID
		result.URL = matches[0].URL
	}
	return result, nil
}

func validatedDescriptor(descriptor attempt.Descriptor) (domain.PullRequestRef, *domain.Failure) {
	invalid := func(message string) (domain.PullRequestRef, *domain.Failure) {
		return domain.PullRequestRef{}, domain.NewFailure(
			domain.CodeInvalidArguments,
			message,
			nil,
			nil,
		)
	}

	if descriptor.SchemaVersion != attempt.DescriptorSchemaVersion {
		return invalid(fmt.Sprintf(
			"Reconciliation descriptor schemaVersion must be %d.",
			attempt.DescriptorSchemaVersion,
		))
	}
	if !domain.IsSupportedHost(descriptor.Host) {
		return domain.PullRequestRef{}, domain.NewFailure(
			domain.CodeUnsupportedHost,
			"Only github.com reconciliation descriptors are supported.",
			map[string]any{"host": descriptor.Host},
			nil,
		)
	}
	owner, repository, ok := strings.Cut(descriptor.Repository, "/")
	if !ok ||
		strings.Contains(repository, "/") ||
		!domain.ValidRepositoryOwner(owner) ||
		!domain.ValidRepositoryName(repository) {
		return invalid("The reconciliation descriptor repository must be OWNER/REPOSITORY.")
	}
	if descriptor.PullRequest <= 0 {
		return invalid("The reconciliation descriptor pullRequest must be positive.")
	}
	if !domain.ValidCommitSHA(descriptor.BaseSHA) ||
		!domain.ValidCommitSHA(descriptor.HeadSHA) {
		return invalid("The reconciliation descriptor must contain full base and head commit SHAs.")
	}
	if descriptor.Event != domain.ReviewEventComment {
		return invalid("The reconciliation descriptor event must be COMMENT.")
	}
	if !domain.ValidSHA256Hex(descriptor.ReviewBodySHA256) ||
		!domain.ValidSHA256Hex(descriptor.RequestSHA256) {
		return invalid("The reconciliation descriptor contains an invalid SHA-256 digest.")
	}
	if descriptor.AttemptStartedAt.IsZero() {
		return invalid("The reconciliation descriptor attemptStartedAt is required.")
	}
	if len(descriptor.Suggestions) == 0 || len(descriptor.Suggestions) > domain.MaxSuggestions {
		return invalid("The reconciliation descriptor must contain between 1 and 100 suggestions.")
	}
	for _, suggestion := range descriptor.Suggestions {
		if !domain.ValidRepositoryPath(suggestion.Path) ||
			suggestion.EndLine <= 0 ||
			(suggestion.StartLine != nil &&
				(*suggestion.StartLine <= 0 || *suggestion.StartLine >= suggestion.EndLine)) ||
			suggestion.Side != string(domain.SideRight) ||
			!domain.ValidSHA256Hex(suggestion.BodySHA256) {
			return invalid("The reconciliation descriptor contains an invalid suggestion.")
		}
	}

	return domain.PullRequestRef{
		Repository: domain.Repository{
			Host:  domain.SupportedHost,
			Owner: owner,
			Name:  repository,
		},
		Number: descriptor.PullRequest,
	}, nil
}

func matchingReviewMetadata(
	descriptor attempt.Descriptor,
	review Review,
	notBefore time.Time,
	notAfter time.Time,
) bool {
	if review.ID <= 0 ||
		review.URL == "" ||
		!strings.EqualFold(review.State, domain.ReviewStateCommented) ||
		review.SubmittedAt.Before(notBefore) ||
		review.SubmittedAt.After(notAfter) ||
		!strings.EqualFold(review.CommitSHA, descriptor.HeadSHA) {
		return false
	}
	return strings.EqualFold(suggestion.BodySHA256(review.Body), descriptor.ReviewBodySHA256)
}

func matchingCommentSet(descriptor attempt.Descriptor, comments []ReviewComment) bool {
	if len(comments) != len(descriptor.Suggestions) {
		return false
	}

	expected := make(map[commentKey]int, len(descriptor.Suggestions))
	for _, suggestion := range descriptor.Suggestions {
		key := commentKeyFromDescriptor(suggestion)
		expected[key]++
	}
	for _, comment := range comments {
		if !strings.EqualFold(comment.CommitSHA, descriptor.HeadSHA) {
			return false
		}
		key := commentKeyFromComment(comment)
		if expected[key] == 0 {
			return false
		}
		expected[key]--
	}
	return true
}

type commentKey struct {
	Path      string
	StartLine int
	HasStart  bool
	EndLine   int
	StartSide string
	Side      string
	BodySHA   string
}

func commentKeyFromDescriptor(suggestion attempt.Suggestion) commentKey {
	key := commentKey{
		Path:     suggestion.Path,
		EndLine:  suggestion.EndLine,
		Side:     suggestion.Side,
		BodySHA:  strings.ToLower(suggestion.BodySHA256),
		HasStart: suggestion.StartLine != nil,
	}
	if suggestion.StartLine != nil {
		key.StartLine = *suggestion.StartLine
		key.StartSide = string(domain.SideRight)
	}
	return key
}

func commentKeyFromComment(comment ReviewComment) commentKey {
	key := commentKey{
		Path:      comment.Path,
		EndLine:   comment.EndLine,
		Side:      comment.Side,
		BodySHA:   suggestion.BodySHA256(comment.Body),
		HasStart:  comment.StartLine != nil,
		StartSide: comment.StartSide,
	}
	if comment.StartLine != nil {
		key.StartLine = *comment.StartLine
	}
	return key
}
