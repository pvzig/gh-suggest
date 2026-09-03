package create

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/pvzig/gh-suggest/internal/attempt"
	pulldiff "github.com/pvzig/gh-suggest/internal/diff"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/suggestion"
)

const (
	maxAggregateReplacementBytes = suggestion.MaxReplacementBytes
	maxAggregateProseBytes       = suggestion.MaxNoteBytes
)

// Service validates and optionally posts exactly one grouped review.
type Service struct {
	resolver PullRequestResolver
	pulls    PullRequestReader
	diffs    DiffReader
	reviews  ReviewCreator
	now      func() time.Time
}

// NewService wires the ports the create use case depends on.
func NewService(
	resolver PullRequestResolver,
	pulls PullRequestReader,
	diffs DiffReader,
	reviews ReviewCreator,
) *Service {
	return &Service{
		resolver: resolver,
		pulls:    pulls,
		diffs:    diffs,
		reviews:  reviews,
		now:      time.Now,
	}
}

// Execute prepares one ordered grouped review. Dry-run and write paths share
// every read and validation step through the final metadata snapshot.
func (service *Service) Execute(ctx context.Context, request Request) (Result, error) {
	prepared, failure := validateLocalRequest(request)
	if failure != nil {
		return Result{}, failure
	}

	ref, err := service.resolver.Resolve(ctx, request.Selector, request.Repository)
	if err != nil {
		return Result{}, domain.NormalizeFailure(
			ctx,
			err,
			domain.CodeTargetNotFound,
			"The pull request could not be resolved.",
			"The operation was cancelled.",
		)
	}

	initial, err := service.observePullRequest(
		ctx,
		ref,
		request,
		"The pull request could not be read.",
	)
	if err != nil {
		return Result{}, err
	}
	rangeErr := service.verifyRanges(
		ctx,
		ref,
		prepared.suggestions,
		initial.ChangedFiles,
	)
	if rangeErr != nil {
		// A pull request can advance while its unpinned diff is being fetched.
		// Re-read metadata before returning the range failure so a range checked
		// against a newer diff is reported as stale instead of not commentable.
		confirmed, err := service.observePullRequest(
			ctx,
			ref,
			request,
			"The pull request could not be re-read after diff validation failed.",
		)
		if err != nil {
			return Result{}, err
		}
		if failure := compareSnapshots(initial, confirmed); failure != nil {
			return Result{}, failure
		}
		return Result{}, rangeErr
	}

	comments := prepared.reviewComments()

	// The second read closes the window between validating every diff range and
	// the single review write. A base or head that moved invalidates the complete
	// grouped request.
	confirmed, err := service.observePullRequest(
		ctx,
		ref,
		request,
		"The pull request could not be re-read before completion.",
	)
	if err != nil {
		return Result{}, err
	}
	if failure := compareSnapshots(initial, confirmed); failure != nil {
		return Result{}, failure
	}

	requestDigest, failure := reviewRequestDigest(
		ref,
		confirmed,
		prepared.body.Content(),
		comments,
	)
	if failure != nil {
		return Result{}, failure
	}

	result := newResult(ref, confirmed, prepared, requestDigest)
	if request.DryRun {
		return result, nil
	}

	reviewRequest := ReviewRequest{
		Ref:           ref,
		BaseSHA:       confirmed.BaseSHA,
		HeadSHA:       confirmed.HeadSHA,
		Event:         domain.ReviewEventComment,
		Body:          prepared.body.Content(),
		Comments:      comments,
		RequestSHA256: requestDigest,
	}
	return service.post(ctx, reviewRequest, result)
}

// observePullRequest reads one metadata snapshot and holds it to every
// invariant a write depends on: completeness, open state, and any exact head
// SHA the caller supplied as an optional snapshot guard.
func (service *Service) observePullRequest(
	ctx context.Context,
	ref domain.PullRequestRef,
	request Request,
	readFailureMessage string,
) (domain.PullRequest, error) {
	metadata, err := service.pulls.ReadPullRequest(ctx, ref)
	if err != nil {
		return domain.PullRequest{}, domain.NormalizeFailure(
			ctx,
			err,
			domain.CodeGitHubAPIError,
			readFailureMessage,
			"The operation was cancelled.",
		)
	}
	if failure := validateMetadata(ref, metadata); failure != nil {
		return domain.PullRequest{}, failure
	}
	if request.ExpectedHeadSHA != "" &&
		!strings.EqualFold(metadata.HeadSHA, request.ExpectedHeadSHA) {
		return domain.PullRequest{}, staleHeadFailure(request.ExpectedHeadSHA, metadata.HeadSHA)
	}
	return metadata, nil
}

// verifyRanges parses one complete diff and proves every requested range is
// visible on its right side before any write can occur.
func (service *Service) verifyRanges(
	ctx context.Context,
	ref domain.PullRequestRef,
	suggestions []preparedSuggestion,
	changedFiles int,
) error {
	rawDiff, err := service.diffs.ReadDiff(ctx, ref)
	if err != nil {
		return domain.NormalizeFailure(
			ctx,
			err,
			domain.CodeRangeNotCommentable,
			"The pull-request diff could not be verified.",
			"The operation was cancelled.",
		)
	}
	parsedDiff, err := pulldiff.Parse(rawDiff, changedFiles)
	if err != nil {
		return rangeFailure(err)
	}
	for index, prepared := range suggestions {
		request := prepared.request
		if err := parsedDiff.ValidateRightRange(
			request.Path,
			int64(startLine(request)),
			int64(request.EndLine),
		); err != nil {
			failure := rangeFailure(err)
			if failure.Details == nil {
				failure.Details = make(map[string]any)
			}
			failure.Details["suggestionIndex"] = index
			return failure
		}
	}
	return nil
}

// post performs the single external write and promotes the validated result to
// a created one.
func (service *Service) post(
	ctx context.Context,
	request ReviewRequest,
	result Result,
) (Result, error) {
	attemptStartedAt := service.now().UTC()
	review, err := service.reviews.CreateReview(ctx, request)
	if err != nil {
		return Result{}, domain.NormalizeFailure(
			ctx,
			err,
			domain.CodeGitHubAPIError,
			"GitHub did not create the suggestion review.",
			"The operation was cancelled.",
		)
	}
	if review.ID <= 0 || review.URL == "" {
		return Result{}, NewAmbiguousWriteFailure(
			request,
			attemptStartedAt,
			"GitHub did not return a definitive created-review response.",
			nil,
		)
	}

	result.Posted = true
	result.ReviewID = review.ID
	result.URL = review.URL
	return result, nil
}

func compareSnapshots(initial domain.PullRequest, confirmed domain.PullRequest) *domain.Failure {
	if confirmed.BaseSHA == initial.BaseSHA && confirmed.HeadSHA == initial.HeadSHA {
		return nil
	}
	return domain.NewFailure(
		domain.CodeStalePullRequest,
		"The pull request base or head changed after diff validation.",
		map[string]any{
			"initialBaseSHA": initial.BaseSHA,
			"actualBaseSHA":  confirmed.BaseSHA,
			"initialHeadSHA": initial.HeadSHA,
			"actualHeadSHA":  confirmed.HeadSHA,
		},
		nil,
	)
}

func reviewRequestDigest(
	ref domain.PullRequestRef,
	confirmed domain.PullRequest,
	reviewBody string,
	comments []ReviewComment,
) (string, *domain.Failure) {
	digestComments := make([]suggestion.ReviewCommentDigestInput, len(comments))
	for index, comment := range comments {
		digestComments[index] = suggestion.ReviewCommentDigestInput{
			Path:      comment.Path,
			StartLine: optionalLineCopy(comment.StartLine),
			EndLine:   comment.EndLine,
			Side:      string(domain.SideRight),
			Body:      comment.Body,
		}
	}
	digest, err := suggestion.ComputeReviewRequestSHA256(
		suggestion.ReviewRequestDigestInput{
			Host:        ref.Repository.Host,
			Owner:       ref.Repository.Owner,
			Repository:  ref.Repository.Name,
			PullRequest: ref.Number,
			BaseSHA:     confirmed.BaseSHA,
			HeadSHA:     confirmed.HeadSHA,
			Event:       domain.ReviewEventComment,
			ReviewBody:  reviewBody,
			Comments:    digestComments,
		},
	)
	if err != nil {
		return "", domain.NewFailure(
			domain.CodeInvalidArguments,
			"The validated review request digest could not be computed.",
			nil,
			err,
		)
	}
	return digest, nil
}

func newResult(
	ref domain.PullRequestRef,
	confirmed domain.PullRequest,
	prepared preparedReview,
	requestDigest string,
) Result {
	summaries := make([]SuggestionSummary, len(prepared.suggestions))
	for index, item := range prepared.suggestions {
		metadata := item.replacement.Metadata()
		summaries[index] = SuggestionSummary{
			Path:      item.request.Path,
			StartLine: optionalLineCopy(item.request.StartLine),
			EndLine:   item.request.EndLine,
			Replacement: ReplacementSummary{
				ByteCount: metadata.ByteCount,
				LineCount: metadata.LineCount,
				SHA256:    metadata.SHA256,
			},
			BodySHA256: suggestion.BodySHA256(item.body),
		}
	}

	return Result{
		Host:             ref.Repository.Host,
		Repository:       ref.Repository.String(),
		PullRequest:      ref.Number,
		PullRequestURL:   confirmed.URL,
		BaseSHA:          confirmed.BaseSHA,
		HeadSHA:          confirmed.HeadSHA,
		ReviewBodySHA256: suggestion.BodySHA256(prepared.body.Content()),
		Suggestions:      summaries,
		RequestSHA256:    requestDigest,
	}
}

// startLine reports the first line of a requested range. A one-line suggestion
// has no explicit start line.
func startLine(request Suggestion) int {
	if request.StartLine != nil {
		return *request.StartLine
	}
	return request.EndLine
}

// ValidateLocalRequest verifies all request fields and content that do not
// require repository, authentication, or network access.
func ValidateLocalRequest(request Request) *domain.Failure {
	_, failure := validateLocalRequest(request)
	return failure
}

// ValidateRequestFields verifies review, target, note, and expected-head semantics
// without inspecting replacement bytes. It supports CLI preflight before any
// caller-selected replacement file is opened.
func ValidateRequestFields(request Request) *domain.Failure {
	if failure := validateRequestStructure(request); failure != nil {
		return failure
	}
	_, _, failure := normalizeProse(request)
	return failure
}

type preparedReview struct {
	body        suggestion.ReviewBody
	suggestions []preparedSuggestion
}

type preparedSuggestion struct {
	request     Suggestion
	replacement suggestion.Replacement
	body        string
}

func (prepared preparedReview) reviewComments() []ReviewComment {
	comments := make([]ReviewComment, len(prepared.suggestions))
	for index, item := range prepared.suggestions {
		comments[index] = ReviewComment{
			Body:      item.body,
			Path:      item.request.Path,
			StartLine: optionalLineCopy(item.request.StartLine),
			EndLine:   item.request.EndLine,
		}
	}
	return comments
}

func validateLocalRequest(request Request) (preparedReview, *domain.Failure) {
	if failure := validateRequestStructure(request); failure != nil {
		return preparedReview{}, failure
	}
	body, notes, failure := normalizeProse(request)
	if failure != nil {
		return preparedReview{}, failure
	}

	prepared := preparedReview{
		body:        body,
		suggestions: make([]preparedSuggestion, len(request.Suggestions)),
	}
	aggregateReplacementBytes := 0
	for index, item := range request.Suggestions {
		replacement, err := suggestion.NormalizeReplacement(item.Replacement)
		if err != nil {
			return preparedReview{}, domain.NewFailure(
				domain.CodeInvalidArguments,
				"The replacement input is invalid.",
				map[string]any{
					"field":           "replacement",
					"suggestionIndex": index,
				},
				err,
			)
		}
		aggregateReplacementBytes += replacement.Metadata().ByteCount
		if aggregateReplacementBytes > maxAggregateReplacementBytes {
			return preparedReview{}, domain.NewFailure(
				domain.CodeInvalidArguments,
				"Review replacements exceed the 1 MiB aggregate limit.",
				map[string]any{
					"maximumBytes": maxAggregateReplacementBytes,
					"actualBytes":  aggregateReplacementBytes,
				},
				nil,
			)
		}

		requestCopy := item
		requestCopy.StartLine = optionalLineCopy(item.StartLine)
		// The normalized replacement and rendered body are the only prepared
		// forms needed after validation. Do not retain a second raw copy.
		requestCopy.Replacement = nil
		prepared.suggestions[index] = preparedSuggestion{
			request:     requestCopy,
			replacement: replacement,
			body:        suggestion.RenderMarkdown(notes[index], replacement),
		}
	}
	return prepared, nil
}

func normalizeProse(
	request Request,
) (suggestion.ReviewBody, []suggestion.Note, *domain.Failure) {
	body, err := suggestion.NormalizeReviewBody(request.ReviewBody)
	if err != nil {
		return suggestion.ReviewBody{}, nil, domain.NewFailure(
			domain.CodeInvalidArguments,
			"The review body is invalid.",
			map[string]any{"field": "reviewBody"},
			err,
		)
	}
	if strings.TrimSpace(body.Content()) == "" {
		return suggestion.ReviewBody{}, nil, domain.NewFailure(
			domain.CodeInvalidArguments,
			"The review body must not be empty.",
			map[string]any{"field": "reviewBody"},
			nil,
		)
	}

	aggregateProseBytes := len(body.Content())
	notes := make([]suggestion.Note, len(request.Suggestions))
	for index, item := range request.Suggestions {
		note, err := suggestion.NormalizeNote(item.Note)
		if err != nil {
			return suggestion.ReviewBody{}, nil, domain.NewFailure(
				domain.CodeInvalidArguments,
				"The suggestion note is invalid.",
				map[string]any{
					"field":           "note",
					"suggestionIndex": index,
				},
				err,
			)
		}
		aggregateProseBytes += len(note.Content())
		if aggregateProseBytes > maxAggregateProseBytes {
			return suggestion.ReviewBody{}, nil, domain.NewFailure(
				domain.CodeInvalidArguments,
				"Review prose exceeds the 64 KiB aggregate limit.",
				map[string]any{
					"maximumBytes": maxAggregateProseBytes,
					"actualBytes":  aggregateProseBytes,
				},
				nil,
			)
		}
		notes[index] = note
	}
	return body, notes, nil
}

func validateRequestStructure(request Request) *domain.Failure {
	if len(request.Suggestions) == 0 || len(request.Suggestions) > MaxSuggestions {
		return domain.NewFailure(
			domain.CodeInvalidArguments,
			"A review requires between 1 and 100 suggestions.",
			map[string]any{
				"suggestionCount": len(request.Suggestions),
				"maximum":         MaxSuggestions,
			},
			nil,
		)
	}
	for index, item := range request.Suggestions {
		if !domain.ValidRepositoryPath(item.Path) {
			return domain.NewFailure(
				domain.CodeInvalidArguments,
				"Suggestion paths must be clean repository-relative paths.",
				map[string]any{
					"path":            item.Path,
					"suggestionIndex": index,
				},
				nil,
			)
		}
		if item.EndLine <= 0 {
			return domain.NewFailure(
				domain.CodeInvalidArguments,
				"Suggestion end lines must be positive integers.",
				map[string]any{
					"endLine":         item.EndLine,
					"suggestionIndex": index,
				},
				nil,
			)
		}
		if item.StartLine != nil &&
			(*item.StartLine <= 0 || *item.StartLine >= item.EndLine) {
			return domain.NewFailure(
				domain.CodeInvalidArguments,
				"Suggestion start lines must be positive and less than their end lines.",
				map[string]any{
					"startLine":       *item.StartLine,
					"endLine":         item.EndLine,
					"suggestionIndex": index,
				},
				nil,
			)
		}
	}
	if failure := overlappingRangeFailure(request.Suggestions); failure != nil {
		return failure
	}

	if request.ExpectedHeadSHA != "" && !domain.ValidCommitSHA(request.ExpectedHeadSHA) {
		return domain.NewFailure(
			domain.CodeInvalidArguments,
			"--head-sha must be a 40-character hexadecimal commit SHA.",
			nil,
			nil,
		)
	}
	return nil
}

func overlappingRangeFailure(requests []Suggestion) *domain.Failure {
	for firstIndex := range requests {
		first := requests[firstIndex]
		firstStart := startLine(first)
		for secondIndex := firstIndex + 1; secondIndex < len(requests); secondIndex++ {
			second := requests[secondIndex]
			if first.Path != second.Path {
				continue
			}
			secondStart := startLine(second)
			if firstStart > second.EndLine || secondStart > first.EndLine {
				continue
			}
			return domain.NewFailure(
				domain.CodeInvalidArguments,
				"Suggestions in the same file must not target overlapping ranges.",
				map[string]any{
					"path":        first.Path,
					"firstIndex":  firstIndex,
					"firstStart":  firstStart,
					"firstEnd":    first.EndLine,
					"secondIndex": secondIndex,
					"secondStart": secondStart,
					"secondEnd":   second.EndLine,
				},
				nil,
			)
		}
	}
	return nil
}

func validateMetadata(ref domain.PullRequestRef, metadata domain.PullRequest) *domain.Failure {
	if metadata.Ref != ref ||
		metadata.URL == "" ||
		metadata.BaseSHA == "" ||
		metadata.HeadSHA == "" ||
		metadata.ChangedFiles < 0 {
		return domain.NewFailure(
			domain.CodeGitHubAPIError,
			"GitHub returned incomplete pull-request metadata.",
			nil,
			nil,
		)
	}
	if metadata.State != domain.StateOpen {
		return domain.NewFailure(
			domain.CodeTargetNotFound,
			"The pull request is not open.",
			map[string]any{"state": metadata.State},
			nil,
		)
	}
	return nil
}

// NewAmbiguousWriteFailure reports a review write whose outcome cannot be
// proven. The nested descriptor is safe to save as JSON and supplies every
// field the read-only reconciliation command needs without exposing prose or
// replacement content.
func NewAmbiguousWriteFailure(
	request ReviewRequest,
	attemptStartedAt time.Time,
	message string,
	cause error,
) *domain.Failure {
	reconciliationSuggestions := make(
		[]attempt.Suggestion,
		len(request.Comments),
	)
	for index, comment := range request.Comments {
		reconciliationSuggestions[index] = attempt.Suggestion{
			Path:       comment.Path,
			StartLine:  optionalLineCopy(comment.StartLine),
			EndLine:    comment.EndLine,
			Side:       string(domain.SideRight),
			BodySHA256: suggestion.BodySHA256(comment.Body),
		}
	}
	descriptor := attempt.Descriptor{
		SchemaVersion:    attempt.DescriptorSchemaVersion,
		Host:             request.Ref.Repository.Host,
		Repository:       request.Ref.Repository.String(),
		PullRequest:      request.Ref.Number,
		BaseSHA:          request.BaseSHA,
		HeadSHA:          request.HeadSHA,
		Event:            request.Event,
		ReviewBodySHA256: suggestion.BodySHA256(request.Body),
		Suggestions:      reconciliationSuggestions,
		RequestSHA256:    request.RequestSHA256,
		AttemptStartedAt: attemptStartedAt.UTC(),
	}
	return domain.NewFailure(
		domain.CodeWriteOutcomeUnknown,
		message,
		map[string]any{
			"reconciliation": descriptor,
			"guidance": "Save the JSON error and run gh suggest reconcile " +
				"--attempt-file FILE; do not retry the write.",
		},
		cause,
	)
}

func staleHeadFailure(expected string, actual string) *domain.Failure {
	return domain.NewFailure(
		domain.CodeStalePullRequest,
		"The pull request head differs from the validated head.",
		map[string]any{
			"expectedHeadSHA": expected,
			"actualHeadSHA":   actual,
		},
		nil,
	)
}

func rangeFailure(err error) *domain.Failure {
	validationError, ok := errors.AsType[*pulldiff.ValidationError](err)
	if !ok {
		return domain.NewFailure(
			domain.CodeRangeNotCommentable,
			"The selected right-side range could not be verified.",
			nil,
			err,
		)
	}
	return domain.NewFailure(
		domain.CodeRangeNotCommentable,
		validationError.Error(),
		validationDetails(validationError.Details),
		err,
	)
}

// validationDetails projects diff validation details through their own JSON
// tags, so every populated field reaches the error envelope and omitted fields
// stay omitted without a per-field branch here.
func validationDetails(source pulldiff.Details) map[string]any {
	encoded, err := json.Marshal(source)
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var details map[string]any
		if decoder.Decode(&details) == nil {
			return details
		}
	}

	return map[string]any{"reason": string(source.Reason)}
}

func optionalLineCopy(line *int) *int {
	if line == nil {
		return nil
	}
	value := *line
	return &value
}
