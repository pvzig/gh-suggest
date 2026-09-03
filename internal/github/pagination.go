package github

import (
	"context"
	"net/http"

	"github.com/pvzig/gh-suggest/internal/domain"
)

// readPages owns the bounded pagination mechanics shared by GitHub list
// endpoints. consume runs before the next read so endpoint-specific validation
// still fails without issuing unnecessary requests.
func readPages[Response any](
	ctx context.Context,
	client restClient,
	pathForPage func(int) string,
	readFailureMessage string,
	pageLimitFailureMessage string,
	consume func([]Response) error,
) error {
	for page := 1; page <= maxListPages; page++ {
		var responses []Response
		if err := client.DoWithContext(
			ctx,
			http.MethodGet,
			pathForPage(page),
			nil,
			&responses,
		); err != nil {
			return classifyReadError(ctx, err, readFailureMessage)
		}
		if err := consume(responses); err != nil {
			return err
		}
		if len(responses) < listPageSize {
			return nil
		}
	}

	return domain.NewFailure(
		domain.CodeGitHubAPIError,
		pageLimitFailureMessage,
		map[string]any{
			"maximumPages": maxListPages,
			"pageSize":     listPageSize,
		},
		nil,
	)
}
