package github

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"

	pulldiff "github.com/pvzig/gh-suggest/internal/diff"
	"github.com/pvzig/gh-suggest/internal/domain"
)

// ReadDiff fetches the textual pull-request diff through the dedicated diff
// media-type client and fails closed when the response cannot be bounded.
func (adapter *Adapter) ReadDiff(
	ctx context.Context,
	ref domain.PullRequestRef,
) ([]byte, error) {
	path := repositoryPath(
		ref.Repository.Owner,
		ref.Repository.Name,
		fmt.Sprintf("pulls/%d", ref.Number),
	)
	response, err := adapter.diff.RequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		failure := classifyReadError(
			ctx,
			err,
			"GitHub could not retrieve a complete pull-request diff.",
		)
		switch failure.Code {
		case domain.CodeAuthenticationFailed,
			domain.CodeRateLimited,
			domain.CodePermissionDenied,
			domain.CodeTargetNotFound,
			domain.CodeUnsupportedHost,
			domain.CodeCancelled:
			return nil, failure
		}
		return nil, diffFailure(pulldiff.ReasonDiffUnavailable, failure)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.ContentLength >= pulldiff.MaxDiffBytes {
		return nil, diffFailure(
			pulldiff.ReasonDiffTooLarge,
			domain.NewFailure(
				domain.CodeRangeNotCommentable,
				"The pull-request diff exceeds the supported size limit.",
				map[string]any{
					"contentLength": response.ContentLength,
					"maximumBytes":  pulldiff.MaxDiffBytes,
				},
				nil,
			),
		)
	}

	content, err := io.ReadAll(io.LimitReader(response.Body, pulldiff.MaxDiffBytes))
	if err != nil {
		if failure := domain.ContextCancellationFailure(
			ctx,
			err,
			"The GitHub diff request was cancelled.",
		); failure != nil {
			return nil, failure
		}
		return nil, diffFailure(
			pulldiff.ReasonDiffUnavailable,
			domain.NewFailure(
				domain.CodeRangeNotCommentable,
				"The pull-request diff could not be read completely.",
				nil,
				err,
			),
		)
	}
	// A response that reaches the shared bound may have been truncated by
	// GitHub, so it is rejected on the same boundary the parser enforces.
	if int64(len(content)) >= pulldiff.MaxDiffBytes {
		return nil, diffFailure(
			pulldiff.ReasonDiffTooLarge,
			domain.NewFailure(
				domain.CodeRangeNotCommentable,
				"The pull-request diff exceeds the supported size limit.",
				map[string]any{"maximumBytes": pulldiff.MaxDiffBytes},
				nil,
			),
		)
	}

	return content, nil
}

// diffFailure stamps the machine-readable diff reason last so an inherited
// detail can never displace it.
func diffFailure(reason pulldiff.Reason, failure *domain.Failure) *domain.Failure {
	details := make(map[string]any, len(failure.Details)+1)
	maps.Copy(details, failure.Details)
	details["reason"] = string(reason)
	return domain.NewFailure(
		domain.CodeRangeNotCommentable,
		failure.Message,
		details,
		failure.Cause,
	)
}
