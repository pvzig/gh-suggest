package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/resolve"
)

type pullRequestResponse struct {
	Number       int    `json:"number"`
	HTMLURL      string `json:"html_url"`
	State        string `json:"state"`
	ChangedFiles int    `json:"changed_files"`
	Base         struct {
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo *struct {
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repo"`
	} `json:"head"`
}

// ReadPullRequest retrieves the exact metadata required by stale-state and
// completeness validation.
func (adapter *Adapter) ReadPullRequest(
	ctx context.Context,
	ref domain.PullRequestRef,
) (domain.PullRequest, error) {
	path := repositoryPath(
		ref.Repository.Owner,
		ref.Repository.Name,
		fmt.Sprintf("pulls/%d", ref.Number),
	)
	var response pullRequestResponse
	if err := adapter.json.DoWithContext(ctx, http.MethodGet, path, nil, &response); err != nil {
		return domain.PullRequest{}, classifyReadError(
			ctx,
			err,
			"GitHub could not read the pull request.",
		)
	}
	if response.Number != ref.Number {
		return domain.PullRequest{}, domain.NewFailure(
			domain.CodeGitHubAPIError,
			"GitHub returned metadata for a different pull request.",
			map[string]any{
				"requestedPullRequest": ref.Number,
				"returnedPullRequest":  response.Number,
			},
			nil,
		)
	}
	if response.State != domain.StateOpen {
		return domain.PullRequest{}, domain.NewFailure(
			domain.CodeTargetNotFound,
			"The pull request is not open.",
			map[string]any{
				"pullRequest": ref.Number,
				"state":       response.State,
			},
			nil,
		)
	}

	return domain.PullRequest{
		Ref:          ref,
		URL:          response.HTMLURL,
		State:        response.State,
		BaseSHA:      response.Base.SHA,
		HeadSHA:      response.Head.SHA,
		ChangedFiles: response.ChangedFiles,
	}, nil
}

// ListOpenPullRequests paginates all open pull requests. When qualifiedHead is
// non-empty, GitHub performs the exact OWNER:BRANCH filter.
func (adapter *Adapter) ListOpenPullRequests(
	ctx context.Context,
	repository domain.Repository,
	qualifiedHead string,
) ([]resolve.Candidate, error) {
	var candidates []resolve.Candidate
	err := readPages(
		ctx,
		adapter.json,
		func(page int) string {
			path := repositoryPath(
				repository.Owner,
				repository.Name,
				fmt.Sprintf("pulls?state=open&per_page=%d&page=%d", listPageSize, page),
			)
			if qualifiedHead != "" {
				path += "&head=" + url.QueryEscape(qualifiedHead)
			}
			return path
		},
		"GitHub could not list open pull requests.",
		"The repository has more open pull requests than this command pages through.",
		func(responses []pullRequestResponse) error {
			for _, response := range responses {
				headOwner := ""
				if response.Head.Repo != nil {
					headOwner = response.Head.Repo.Owner.Login
				}
				candidates = append(candidates, resolve.Candidate{
					Number:    response.Number,
					HeadOwner: headOwner,
					HeadRef:   response.Head.Ref,
				})
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return candidates, nil
}
