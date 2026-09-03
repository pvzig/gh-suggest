package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/pvzig/gh-suggest/internal/create"
	"github.com/pvzig/gh-suggest/internal/domain"
)

// maxCreatedReviewResponseBytes bounds the response to the non-idempotent POST.
// One additional byte is read to distinguish an exact-boundary response from overflow.
const maxCreatedReviewResponseBytes = 1 << 20

type createReviewRequest struct {
	CommitID string                       `json:"commit_id"`
	Body     string                       `json:"body"`
	Event    string                       `json:"event"`
	Comments []createReviewCommentRequest `json:"comments"`
}

type createReviewCommentRequest struct {
	Body      string `json:"body"`
	Path      string `json:"path"`
	StartLine *int   `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
}

type reviewResponse struct {
	ID          int64  `json:"id"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	CommitID    string `json:"commit_id"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

// singleUseRequestBody deliberately hides bytes.Reader from net/http so it
// cannot synthesize Request.GetBody and replay a non-idempotent POST after an
// HTTP/2 stream reset or GOAWAY. readStarted also distinguishes connection
// setup failures from failures after the transport began consuming the body.
type singleUseRequestBody struct {
	reader      io.Reader
	readStarted atomic.Bool
}

func (body *singleUseRequestBody) Read(destination []byte) (int, error) {
	body.readStarted.Store(true)
	return body.reader.Read(destination)
}

// CreateReview performs exactly one grouped-review POST. Failures before the
// transport consumes any body bytes are definitive; every later transport
// failure, server error, unexpected success status, or incomplete response is
// ambiguous because GitHub exposes no idempotency key.
func (adapter *Adapter) CreateReview(
	ctx context.Context,
	request create.ReviewRequest,
) (create.CreatedReview, error) {
	if err := ctx.Err(); err != nil {
		return create.CreatedReview{}, domain.NewFailure(
			domain.CodeCancelled,
			"The operation was cancelled before the suggestion review write began.",
			nil,
			err,
		)
	}
	if request.Event != domain.ReviewEventComment {
		return create.CreatedReview{}, domain.NewFailure(
			domain.CodeInvalidArguments,
			"Only COMMENT suggestion reviews may be created.",
			map[string]any{"event": request.Event},
			nil,
		)
	}

	comments := make([]createReviewCommentRequest, len(request.Comments))
	for index, comment := range request.Comments {
		comments[index] = createReviewCommentRequest{
			Body: comment.Body,
			Path: comment.Path,
			Line: comment.EndLine,
			Side: string(domain.SideRight),
		}
		if comment.StartLine != nil {
			comments[index].StartLine = comment.StartLine
			comments[index].StartSide = string(domain.SideRight)
		}
	}
	payload := createReviewRequest{
		CommitID: request.HeadSHA,
		Body:     request.Body,
		Event:    request.Event,
		Comments: comments,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return create.CreatedReview{}, domain.NewFailure(
			domain.CodeInvalidArguments,
			"The suggestion review request could not be encoded.",
			nil,
			err,
		)
	}

	attemptStartedAt := adapter.now().UTC()
	path := repositoryPath(
		request.Ref.Repository.Owner,
		request.Ref.Repository.Name,
		fmt.Sprintf("pulls/%d/reviews", request.Ref.Number),
	)
	body := &singleUseRequestBody{reader: bytes.NewReader(encoded)}
	response, err := adapter.json.RequestWithContext(
		ctx,
		http.MethodPost,
		path,
		body,
	)
	if err != nil {
		if httpError, ok := errors.AsType[*api.HTTPError](err); ok {
			if httpError.StatusCode >= http.StatusInternalServerError {
				return create.CreatedReview{}, ambiguousWriteFailure(
					request,
					attemptStartedAt,
					httpError,
				)
			}
			return create.CreatedReview{}, classifyHTTPError(
				httpError,
				"GitHub rejected the suggestion review.",
			)
		}
		if requestDefinitelyNotSent(body, err) {
			return create.CreatedReview{}, domain.NewFailure(
				domain.CodeGitHubAPIError,
				"The suggestion review could not be sent to GitHub.",
				map[string]any{"reason": "connection_failed_before_write"},
				err,
			)
		}
		return create.CreatedReview{}, ambiguousWriteFailure(request, attemptStartedAt, err)
	}
	if response == nil {
		return create.CreatedReview{}, ambiguousWriteFailure(
			request,
			attemptStartedAt,
			errors.New("created-review response was missing"),
		)
	}
	if response.StatusCode != http.StatusOK {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return create.CreatedReview{}, ambiguousWriteFailure(
			request,
			attemptStartedAt,
			fmt.Errorf(
				"created-review response returned status %d instead of %d",
				response.StatusCode,
				http.StatusOK,
			),
		)
	}

	var responseBody []byte
	if response.Body != nil {
		responseBody, err = io.ReadAll(io.LimitReader(
			response.Body,
			maxCreatedReviewResponseBytes+1,
		))
		_ = response.Body.Close()
		if err == nil && len(responseBody) > maxCreatedReviewResponseBytes {
			err = fmt.Errorf(
				"created-review response exceeds the %d-byte limit",
				maxCreatedReviewResponseBytes,
			)
		}
	} else {
		err = errors.New("created-review response body was missing")
	}
	if err != nil {
		return create.CreatedReview{}, ambiguousWriteFailure(
			request,
			attemptStartedAt,
			err,
		)
	}

	var createdReview reviewResponse
	if err := json.Unmarshal(responseBody, &createdReview); err != nil {
		return create.CreatedReview{}, ambiguousWriteFailure(
			request,
			attemptStartedAt,
			err,
		)
	}
	if createdReview.ID <= 0 || createdReview.HTMLURL == "" {
		return create.CreatedReview{}, ambiguousWriteFailure(
			request,
			attemptStartedAt,
			errors.New("created-review response did not contain a positive ID and URL"),
		)
	}

	return create.CreatedReview{
		ID:  createdReview.ID,
		URL: createdReview.HTMLURL,
	}, nil
}

func requestDefinitelyNotSent(body *singleUseRequestBody, err error) bool {
	return !errors.Is(err, errWriteRedirectRejected) && !body.readStarted.Load()
}

func ambiguousWriteFailure(
	request create.ReviewRequest,
	attemptStartedAt time.Time,
	cause error,
) *domain.Failure {
	return create.NewAmbiguousWriteFailure(
		request,
		attemptStartedAt,
		"The suggestion review may have been created, but GitHub did not return a definitive response.",
		cause,
	)
}
