package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestReadPullRequestMapsEndpointDTO(t *testing.T) {
	t.Parallel()

	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet ||
			request.URL.Path != "/repos/octo/example/pulls/42" {
			t.Fatalf("request = %s %s, want pull-request metadata GET", request.Method, request.URL.Path)
		}
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			`{
				"number": 42,
				"html_url": "https://github.com/octo/example/pull/42",
				"state": "open",
				"changed_files": 7,
				"base": {"sha": "base-sha"},
				"head": {
					"sha": "head-sha",
					"ref": "feature/suggestion",
					"repo": {"owner": {"login": "fork-owner"}}
				}
			}`,
		), nil
	}))

	ref := testPullRequestRef()
	pullRequest, err := adapter.ReadPullRequest(context.Background(), ref)
	if err != nil {
		t.Fatalf("ReadPullRequest() error = %v", err)
	}
	if pullRequest.Ref != ref {
		t.Errorf("Ref = %#v, want %#v", pullRequest.Ref, ref)
	}
	if pullRequest.URL != "https://github.com/octo/example/pull/42" {
		t.Errorf("URL = %q", pullRequest.URL)
	}
	if pullRequest.State != "open" {
		t.Errorf("State = %q, want open", pullRequest.State)
	}
	if pullRequest.BaseSHA != "base-sha" || pullRequest.HeadSHA != "head-sha" {
		t.Errorf(
			"base/head = %q/%q, want base-sha/head-sha",
			pullRequest.BaseSHA,
			pullRequest.HeadSHA,
		)
	}
	if pullRequest.ChangedFiles != 7 {
		t.Errorf("ChangedFiles = %d, want 7", pullRequest.ChangedFiles)
	}
}

func TestReadPullRequestAllowsDeletedHeadRepository(t *testing.T) {
	t.Parallel()

	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			`{
				"number": 42,
				"html_url": "https://github.com/octo/example/pull/42",
				"state": "open",
				"changed_files": 1,
				"base": {"sha": "base-sha"},
				"head": {"sha": "head-sha", "ref": "feature", "repo": null}
			}`,
		), nil
	}))

	pullRequest, err := adapter.ReadPullRequest(context.Background(), testPullRequestRef())
	if err != nil {
		t.Fatalf("ReadPullRequest() error = %v", err)
	}
	if pullRequest.HeadSHA != "head-sha" {
		t.Fatalf("HeadSHA = %q, want head-sha", pullRequest.HeadSHA)
	}
}

func TestReadPullRequestRejectsMetadataForAnotherPullRequest(t *testing.T) {
	t.Parallel()

	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			`{
				"number": 43,
				"html_url": "https://github.com/octo/example/pull/43",
				"state": "open",
				"changed_files": 1,
				"base": {"sha": "base-sha"},
				"head": {"sha": "head-sha", "ref": "feature", "repo": null}
			}`,
		), nil
	}))

	_, err := adapter.ReadPullRequest(context.Background(), testPullRequestRef())
	failure := assertFailure(t, err, domain.CodeGitHubAPIError)
	if failure.Details["requestedPullRequest"] != 42 ||
		failure.Details["returnedPullRequest"] != 43 {
		t.Fatalf("details = %#v, want requested 42 and returned 43", failure.Details)
	}
}

func TestReadPullRequestFollowsRenamedRepositoryRedirect(t *testing.T) {
	t.Parallel()

	requests := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Path == "/repos/octo/example/pulls/42" {
			redirect := textResponse(request, http.StatusMovedPermanently, "application/json", "")
			redirect.Header.Set("Location", "https://api.github.com/repos/octo/renamed/pulls/42")
			return redirect, nil
		}
		if request.URL.Path != "/repos/octo/renamed/pulls/42" {
			t.Fatalf("request path = %s, want the redirect target", request.URL.Path)
		}
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			`{
				"number": 42,
				"html_url": "https://github.com/octo/renamed/pull/42",
				"state": "open",
				"changed_files": 1,
				"base": {"sha": "base-sha"},
				"head": {"sha": "head-sha", "ref": "feature", "repo": null}
			}`,
		), nil
	}))

	pullRequest, err := adapter.ReadPullRequest(context.Background(), testPullRequestRef())
	if err != nil {
		t.Fatalf("ReadPullRequest() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if pullRequest.URL != "https://github.com/octo/renamed/pull/42" {
		t.Fatalf("URL = %q, want the renamed repository URL", pullRequest.URL)
	}
}

func TestReadPullRequestRejectsNonOpenState(t *testing.T) {
	t.Parallel()

	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			`{"number": 42, "state": "closed"}`,
		), nil
	}))

	_, err := adapter.ReadPullRequest(context.Background(), testPullRequestRef())
	failure := assertFailure(t, err, domain.CodeTargetNotFound)
	if failure.Details["state"] != "closed" {
		t.Fatalf("failure state = %#v, want closed", failure.Details["state"])
	}
}

func TestListOpenPullRequestsPaginatesAndMapsEndpointDTOs(t *testing.T) {
	t.Parallel()

	firstPage := make([]map[string]any, 100)
	for index := range firstPage {
		number := index + 1
		firstPage[index] = map[string]any{
			"number":   number,
			"html_url": "https://github.com/octo/example/pull/" + strconv.Itoa(number),
			"head": map[string]any{
				"ref": "feature-" + strconv.Itoa(number),
				"repo": map[string]any{
					"owner": map[string]any{"login": "fork-owner"},
				},
			},
		}
	}
	secondPage := []map[string]any{{
		"number":   101,
		"html_url": "https://github.com/octo/example/pull/101",
		"head": map[string]any{
			"ref":  "feature-101",
			"repo": nil,
		},
	}}
	firstJSON, err := json.Marshal(firstPage)
	if err != nil {
		t.Fatalf("Marshal(firstPage) error = %v", err)
	}
	secondJSON, err := json.Marshal(secondPage)
	if err != nil {
		t.Fatalf("Marshal(secondPage) error = %v", err)
	}

	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if request.Method != http.MethodGet ||
			request.URL.Path != "/repos/octo/example/pulls" {
			t.Fatalf("request = %s %s, want pull-request list GET", request.Method, request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("state") != "open" || query.Get("per_page") != "100" {
			t.Fatalf("query = %q, want state=open and per_page=100", request.URL.RawQuery)
		}
		if query.Get("head") != "fork-owner:feature/with space" {
			t.Fatalf("head query = %q, want decoded qualified head", query.Get("head"))
		}

		switch requestCount {
		case 1:
			if query.Get("page") != "1" {
				t.Fatalf("first page query = %q", request.URL.RawQuery)
			}
			return textResponse(
				request,
				http.StatusOK,
				"application/json",
				string(firstJSON),
			), nil
		case 2:
			if query.Get("page") != "2" {
				t.Fatalf("second page query = %q", request.URL.RawQuery)
			}
			return textResponse(
				request,
				http.StatusOK,
				"application/json",
				string(secondJSON),
			), nil
		default:
			t.Fatalf("unexpected pagination request %d", requestCount)
			return nil, nil
		}
	}))

	candidates, err := adapter.ListOpenPullRequests(
		context.Background(),
		testPullRequestRef().Repository,
		"fork-owner:feature/with space",
	)
	if err != nil {
		t.Fatalf("ListOpenPullRequests() error = %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
	if len(candidates) != 101 {
		t.Fatalf("candidate count = %d, want 101", len(candidates))
	}
	if got := candidates[0]; got.Number != 1 ||
		got.HeadOwner != "fork-owner" ||
		got.HeadRef != "feature-1" {
		t.Errorf("first candidate = %#v", got)
	}
	if got := candidates[100]; got.Number != 101 ||
		got.HeadOwner != "" ||
		got.HeadRef != "feature-101" {
		t.Errorf("last candidate = %#v", got)
	}
}

func TestListOpenPullRequestsStopsAtThePageBound(t *testing.T) {
	t.Parallel()

	fullPage := make([]map[string]any, listPageSize)
	for index := range fullPage {
		number := index + 1
		fullPage[index] = map[string]any{
			"number":   number,
			"html_url": "https://github.com/octo/example/pull/" + strconv.Itoa(number),
			"head":     map[string]any{"ref": "feature-" + strconv.Itoa(number)},
		}
	}
	fullJSON, err := json.Marshal(fullPage)
	if err != nil {
		t.Fatalf("Marshal(fullPage) error = %v", err)
	}

	// Every page is full, so pagination only ends when the bound is reached.
	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		return textResponse(request, http.StatusOK, "application/json", string(fullJSON)), nil
	}))

	_, err = adapter.ListOpenPullRequests(
		context.Background(),
		testPullRequestRef().Repository,
		"",
	)
	_ = assertFailure(t, err, domain.CodeGitHubAPIError)
	if requestCount != maxListPages {
		t.Fatalf("request count = %d, want %d", requestCount, maxListPages)
	}
}

func TestListOpenPullRequestsOmitsEmptyHeadFilter(t *testing.T) {
	t.Parallel()

	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if _, present := request.URL.Query()["head"]; present {
			t.Fatalf("query = %q, want no head filter", request.URL.RawQuery)
		}
		return textResponse(request, http.StatusOK, "application/json", "[]"), nil
	}))

	candidates, err := adapter.ListOpenPullRequests(
		context.Background(),
		testPullRequestRef().Repository,
		"",
	)
	if err != nil {
		t.Fatalf("ListOpenPullRequests() error = %v", err)
	}
	if candidates != nil {
		t.Fatalf("candidates = %#v, want nil", candidates)
	}
}

func TestReadPullRequestClassifiesFakeTransportHTTPFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		status           int
		headers          http.Header
		wantCode         domain.Code
		wantRetryAfter   any
		wantRateLimitEnd any
	}{
		{
			name:     "authentication rejected",
			status:   http.StatusUnauthorized,
			wantCode: domain.CodeAuthenticationFailed,
		},
		{
			name:     "authentication challenged",
			status:   http.StatusForbidden,
			headers:  testHTTPHeaders("WWW-Authenticate", `Bearer realm="GitHub"`),
			wantCode: domain.CodeAuthenticationFailed,
		},
		{
			name:     "permission denied",
			status:   http.StatusForbidden,
			wantCode: domain.CodePermissionDenied,
		},
		{
			name:     "target hidden or missing",
			status:   http.StatusNotFound,
			wantCode: domain.CodeTargetNotFound,
		},
		{
			name:   "primary rate limited",
			status: http.StatusForbidden,
			headers: testHTTPHeaders(
				"X-RateLimit-Remaining",
				"0",
				"Retry-After",
				"60",
				"X-RateLimit-Reset",
				"1784810400",
			),
			wantCode:         domain.CodeRateLimited,
			wantRetryAfter:   60,
			wantRateLimitEnd: int64(1784810400),
		},
		{
			name:     "too many requests",
			status:   http.StatusTooManyRequests,
			wantCode: domain.CodeRateLimited,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requestCount := 0
			adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestCount++
				result := textResponse(
					request,
					test.status,
					"application/json",
					`{"message":"GitHub rejected the request"}`,
				)
				for name, values := range test.headers {
					result.Header[name] = append([]string(nil), values...)
				}
				return result, nil
			}))

			_, err := adapter.ReadPullRequest(context.Background(), testPullRequestRef())
			failure := assertFailure(t, err, test.wantCode)
			if requestCount != 1 {
				t.Fatalf("request count = %d, want 1", requestCount)
			}
			if got := failure.Details["status"]; got != test.status {
				t.Errorf("status = %#v, want %d", got, test.status)
			}
			if test.wantRetryAfter != nil &&
				failure.Details["retryAfter"] != test.wantRetryAfter {
				t.Errorf(
					"retryAfter = %#v, want %#v",
					failure.Details["retryAfter"],
					test.wantRetryAfter,
				)
			}
			if test.wantRateLimitEnd != nil &&
				failure.Details["rateLimitReset"] != test.wantRateLimitEnd {
				t.Errorf(
					"rateLimitReset = %#v, want %#v",
					failure.Details["rateLimitReset"],
					test.wantRateLimitEnd,
				)
			}
		})
	}
}
