package github

import (
	"context"
	"fmt"

	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/reconcile"
)

type commentResponse struct {
	Body              string `json:"body"`
	CommitID          string `json:"commit_id"`
	OriginalCommitID  string `json:"original_commit_id"`
	Path              string `json:"path"`
	StartLine         *int   `json:"start_line"`
	OriginalStartLine *int   `json:"original_start_line"`
	StartSide         string `json:"start_side"`
	Line              *int   `json:"line"`
	OriginalLine      *int   `json:"original_line"`
	Side              string `json:"side"`
}

// ListReviewComments reads every inline comment belonging to one review.
// GitHub may return current coordinates after later commits, so original
// coordinates and the original commit take precedence when available.
func (adapter *Adapter) ListReviewComments(
	ctx context.Context,
	ref domain.PullRequestRef,
	reviewID int64,
) ([]reconcile.ReviewComment, error) {
	var comments []reconcile.ReviewComment
	err := readPages(
		ctx,
		adapter.json,
		func(page int) string {
			return repositoryPath(
				ref.Repository.Owner,
				ref.Repository.Name,
				fmt.Sprintf(
					"pulls/%d/reviews/%d/comments?per_page=%d&page=%d",
					ref.Number,
					reviewID,
					listPageSize,
					page,
				),
			)
		},
		"GitHub could not list review comments.",
		"The review has more comments than this command pages through.",
		func(responses []commentResponse) error {
			for _, response := range responses {
				commitSHA := response.OriginalCommitID
				if commitSHA == "" {
					commitSHA = response.CommitID
				}
				startLine := response.OriginalStartLine
				if startLine == nil {
					startLine = response.StartLine
				}
				startSide := response.StartSide
				if startLine != nil && startSide == "" {
					startSide = response.Side
				}
				endLine := 0
				if response.OriginalLine != nil {
					endLine = *response.OriginalLine
				} else if response.Line != nil {
					endLine = *response.Line
				}
				comments = append(comments, reconcile.ReviewComment{
					Body:      response.Body,
					CommitSHA: commitSHA,
					Path:      response.Path,
					StartLine: startLine,
					StartSide: startSide,
					EndLine:   endLine,
					Side:      response.Side,
				})
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return comments, nil
}
