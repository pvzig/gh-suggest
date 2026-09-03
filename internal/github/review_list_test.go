package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestListReviewsPaginatesFiltersCutoffAndCanonicalizesCommentedState(t *testing.T) {
	t.Parallel()

	notBefore := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	firstPage := make([]map[string]any, listPageSize)
	for index := range firstPage {
		firstPage[index] = map[string]any{
			"id":           index + 1,
			"html_url":     "https://github.com/octo/example/pull/42#pullrequestreview-" + strconv.Itoa(index+1),
			"body":         "old",
			"commit_id":    "old-head",
			"state":        "COMMENTED",
			"submitted_at": "2026-07-23T11:59:59Z",
		}
	}
	firstJSON, err := json.Marshal(firstPage)
	if err != nil {
		t.Fatalf("Marshal(firstPage) error = %v", err)
	}

	adapter, requestCount := newPaginatedJSONAdapter(
		t,
		"/repos/octo/example/pulls/42/reviews",
		string(firstJSON),
		`[
					{
						"id": 101,
						"state": "PENDING",
						"submitted_at": "not-a-timestamp"
					},
					{
						"id": 102,
						"html_url": "https://github.com/octo/example/pull/42#pullrequestreview-102",
						"body": "at boundary",
						"commit_id": "head-sha",
						"state": "commented",
						"submitted_at": "2026-07-23T12:00:00Z"
					},
					{
						"id": 103,
						"state": "APPROVED",
						"submitted_at": "not-a-timestamp"
					}
				]`,
	)

	reviews, err := adapter.ListReviews(
		context.Background(),
		testPullRequestRef(),
		notBefore,
	)
	if err != nil {
		t.Fatalf("ListReviews() error = %v", err)
	}
	if requestCount() != 2 {
		t.Fatalf("request count = %d, want 2", requestCount())
	}
	if len(reviews) != 1 {
		t.Fatalf("reviews = %#v, want one COMMENTED review inside cutoff", reviews)
	}
	first := reviews[0]
	if first.ID != 102 ||
		first.URL != "https://github.com/octo/example/pull/42#pullrequestreview-102" ||
		first.Body != "at boundary" ||
		first.CommitSHA != "head-sha" ||
		first.State != domain.ReviewStateCommented ||
		!first.SubmittedAt.Equal(notBefore) {
		t.Fatalf("first review = %#v", first)
	}
}

func TestListReviewsRejectsMalformedSubmittedTimestamp(t *testing.T) {
	t.Parallel()

	responses := make([]map[string]any, listPageSize)
	responses[0] = map[string]any{
		"id":           99,
		"state":        "COMMENTED",
		"submitted_at": "not-a-timestamp",
	}
	for index := 1; index < len(responses); index++ {
		responses[index] = map[string]any{
			"id":    index + 1,
			"state": "PENDING",
		}
	}
	encoded, err := json.Marshal(responses)
	if err != nil {
		t.Fatalf("Marshal(responses) error = %v", err)
	}

	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			string(encoded),
		), nil
	}))

	_, err = adapter.ListReviews(
		context.Background(),
		testPullRequestRef(),
		time.Now(),
	)
	failure := assertFailure(t, err, domain.CodeGitHubAPIError)
	if failure.Details["reviewID"] != int64(99) ||
		failure.Details["submittedAt"] != "not-a-timestamp" {
		t.Fatalf("failure details = %#v", failure.Details)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want validation to stop before page 2", requestCount)
	}
}

func TestListReviewsStopsAtThePageBound(t *testing.T) {
	t.Parallel()

	fullPage := make([]map[string]any, listPageSize)
	for index := range fullPage {
		fullPage[index] = map[string]any{
			"id":    index + 1,
			"state": "PENDING",
		}
	}
	fullJSON, err := json.Marshal(fullPage)
	if err != nil {
		t.Fatalf("Marshal(fullPage) error = %v", err)
	}

	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		return textResponse(request, http.StatusOK, "application/json", string(fullJSON)), nil
	}))

	_, err = adapter.ListReviews(
		context.Background(),
		testPullRequestRef(),
		time.Now(),
	)
	failure := assertFailure(t, err, domain.CodeGitHubAPIError)
	if requestCount != maxListPages {
		t.Fatalf("request count = %d, want %d", requestCount, maxListPages)
	}
	if failure.Details["maximumPages"] != maxListPages ||
		failure.Details["pageSize"] != listPageSize {
		t.Fatalf("failure details = %#v", failure.Details)
	}
}
