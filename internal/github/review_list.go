package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/reconcile"
)

// ListReviews reads the pull request's chronologically ordered reviews and
// returns COMMENTED reviews at or after the caller's cutoff. Other states can
// never reconcile a COMMENT review and may lack a usable submission timestamp.
func (adapter *Adapter) ListReviews(
	ctx context.Context,
	ref domain.PullRequestRef,
	notBefore time.Time,
) ([]reconcile.Review, error) {
	notBefore = notBefore.UTC()
	var reviews []reconcile.Review
	err := readPages(
		ctx,
		adapter.json,
		func(page int) string {
			return repositoryPath(
				ref.Repository.Owner,
				ref.Repository.Name,
				fmt.Sprintf(
					"pulls/%d/reviews?per_page=%d&page=%d",
					ref.Number,
					listPageSize,
					page,
				),
			)
		},
		"GitHub could not list pull-request reviews.",
		"The pull request has more reviews than this command pages through.",
		func(responses []reviewResponse) error {
			for _, response := range responses {
				if !strings.EqualFold(response.State, domain.ReviewStateCommented) {
					continue
				}
				submittedAt, err := time.Parse(time.RFC3339Nano, response.SubmittedAt)
				if err != nil {
					return domain.NewFailure(
						domain.CodeGitHubAPIError,
						"GitHub returned an invalid review submission timestamp.",
						map[string]any{
							"reviewID":    response.ID,
							"submittedAt": response.SubmittedAt,
						},
						err,
					)
				}
				if submittedAt.Before(notBefore) {
					continue
				}
				reviews = append(reviews, reconcile.Review{
					ID:          response.ID,
					URL:         response.HTMLURL,
					Body:        response.Body,
					CommitSHA:   response.CommitID,
					State:       domain.ReviewStateCommented,
					SubmittedAt: submittedAt,
				})
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return reviews, nil
}
