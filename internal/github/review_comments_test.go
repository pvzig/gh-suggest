package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestListReviewCommentsPaginatesAndPrefersOriginalCoordinates(t *testing.T) {
	t.Parallel()

	firstPage := make([]map[string]any, listPageSize)
	for index := range firstPage {
		firstPage[index] = map[string]any{
			"body":      "body-" + strconv.Itoa(index+1),
			"commit_id": "current-head",
			"path":      "Sources/Example.swift",
			"line":      42,
			"side":      "RIGHT",
		}
	}
	firstPage[0]["original_commit_id"] = "review-head"
	firstPage[0]["start_line"] = nil
	firstPage[0]["original_start_line"] = 40
	firstPage[0]["start_side"] = "RIGHT"
	firstPage[0]["line"] = nil
	firstPage[0]["original_line"] = 42
	firstJSON, err := json.Marshal(firstPage)
	if err != nil {
		t.Fatalf("Marshal(firstPage) error = %v", err)
	}

	adapter, requestCount := newPaginatedJSONAdapter(
		t,
		"/repos/octo/example/pulls/42/reviews/99/comments",
		string(firstJSON),
		`[{
					"body": "last body",
					"commit_id": "review-head",
					"path": "Sources/Last.swift",
					"line": 8,
					"side": "RIGHT"
				}]`,
	)

	comments, err := adapter.ListReviewComments(
		context.Background(),
		testPullRequestRef(),
		99,
	)
	if err != nil {
		t.Fatalf("ListReviewComments() error = %v", err)
	}
	if requestCount() != 2 {
		t.Fatalf("request count = %d, want 2", requestCount())
	}
	if len(comments) != 101 {
		t.Fatalf("comment count = %d, want 101", len(comments))
	}
	first := comments[0]
	if first.Body != "body-1" ||
		first.CommitSHA != "review-head" ||
		first.Path != "Sources/Example.swift" ||
		first.StartLine == nil ||
		*first.StartLine != 40 ||
		first.StartSide != "RIGHT" ||
		first.EndLine != 42 ||
		first.Side != "RIGHT" {
		t.Fatalf("first comment = %#v", first)
	}
	last := comments[100]
	if last.Body != "last body" ||
		last.CommitSHA != "review-head" ||
		last.Path != "Sources/Last.swift" ||
		last.StartLine != nil ||
		last.StartSide != "" ||
		last.EndLine != 8 ||
		last.Side != "RIGHT" {
		t.Fatalf("last comment = %#v", last)
	}
}

func TestListReviewCommentsStopsAtThePageBound(t *testing.T) {
	t.Parallel()

	fullPage := make([]map[string]any, listPageSize)
	for index := range fullPage {
		fullPage[index] = map[string]any{
			"body":      "body-" + strconv.Itoa(index),
			"commit_id": "review-head",
			"path":      "Sources/Example.swift",
			"line":      42,
			"side":      "RIGHT",
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

	_, err = adapter.ListReviewComments(context.Background(), testPullRequestRef(), 99)
	failure := assertFailure(t, err, domain.CodeGitHubAPIError)
	if requestCount != maxListPages {
		t.Fatalf("request count = %d, want %d", requestCount, maxListPages)
	}
	if failure.Details["maximumPages"] != maxListPages ||
		failure.Details["pageSize"] != listPageSize {
		t.Fatalf("failure details = %#v", failure.Details)
	}
}
