package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestBoundedHTTPErrorAcceptsExactLimitAndPreservesDiagnostics(t *testing.T) {
	t.Parallel()

	const diagnostic = `{"message":"Validation Failed","errors":["body is invalid"]}`
	request, err := http.NewRequest(http.MethodPost, apiBaseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := textResponse(request, http.StatusUnprocessableEntity, "application/json",
		diagnostic+strings.Repeat(" ", maxErrorResponseBytes-len(diagnostic)))
	defer func() { _ = response.Body.Close() }()
	err = boundedHTTPError(response)
	httpError, ok := errors.AsType[*api.HTTPError](err)
	if !ok || httpError.StatusCode != http.StatusUnprocessableEntity ||
		httpError.Message != "Validation Failed\nbody is invalid" {
		t.Fatalf("HTTP error = %#v", err)
	}
}

func TestBoundedHTTPErrorPreservesStatusAfterBodyReadFailure(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequest(http.MethodPost, apiBaseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := response(request, http.StatusServiceUnavailable, "application/json",
		io.NopCloser(failingReader{err: io.ErrUnexpectedEOF}))
	defer func() { _ = result.Body.Close() }()
	err = boundedHTTPError(result)
	httpError, ok := errors.AsType[*api.HTTPError](err)
	if !ok || httpError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("HTTP error = %#v", err)
	}
}

func TestClassifyHTTPErrorUsesStablePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		message  string
		headers  http.Header
		wantCode domain.Code
	}{
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized,
			wantCode: domain.CodeAuthenticationFailed,
		},
		{
			name:     "explicit authentication challenge precedes forbidden",
			status:   http.StatusForbidden,
			headers:  testHTTPHeaders("WWW-Authenticate", `Bearer realm="GitHub"`),
			wantCode: domain.CodeAuthenticationFailed,
		},
		{
			name:     "forbidden without rate signals",
			status:   http.StatusForbidden,
			message:  "Resource not accessible by integration",
			wantCode: domain.CodePermissionDenied,
		},
		{
			name:     "not found or inaccessible",
			status:   http.StatusNotFound,
			wantCode: domain.CodeTargetNotFound,
		},
		{
			name:     "too many requests",
			status:   http.StatusTooManyRequests,
			wantCode: domain.CodeRateLimited,
		},
		{
			name:     "primary rate limit header",
			status:   http.StatusForbidden,
			headers:  testHTTPHeaders("X-RateLimit-Remaining", "0"),
			wantCode: domain.CodeRateLimited,
		},
		{
			name:     "secondary rate limit message",
			status:   http.StatusForbidden,
			message:  "You have exceeded a secondary rate limit.",
			wantCode: domain.CodeRateLimited,
		},
		{
			name:     "abuse detection message",
			status:   http.StatusForbidden,
			message:  "You have triggered an abuse detection mechanism.",
			wantCode: domain.CodeRateLimited,
		},
		{
			name:     "unclassified API error",
			status:   http.StatusUnprocessableEntity,
			message:  "Validation Failed",
			wantCode: domain.CodeGitHubAPIError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			failure := classifyHTTPError(&api.HTTPError{
				StatusCode: test.status,
				Message:    test.message,
				Headers:    test.headers,
			}, "fallback")
			if failure.Code != test.wantCode {
				t.Fatalf("Code = %q, want %q", failure.Code, test.wantCode)
			}
			if got := failure.Details["status"]; got != test.status {
				t.Fatalf("status detail = %#v, want %d", got, test.status)
			}
			if test.wantCode == domain.CodeGitHubAPIError &&
				failure.Details["githubMessage"] != test.message {
				t.Fatalf(
					"githubMessage = %#v, want %q",
					failure.Details["githubMessage"],
					test.message,
				)
			}
		})
	}
}

func TestClassifyHTTPErrorIncludesRateLimitMetadata(t *testing.T) {
	t.Parallel()

	failure := classifyHTTPError(&api.HTTPError{
		StatusCode: http.StatusForbidden,
		Message:    "secondary rate limit",
		Headers: testHTTPHeaders(
			"Retry-After",
			"120",
			"X-RateLimit-Reset",
			"1784810400",
		),
	}, "fallback")

	if failure.Code != domain.CodeRateLimited {
		t.Fatalf("Code = %q, want %q", failure.Code, domain.CodeRateLimited)
	}
	if got := failure.Details["retryAfter"]; got != 120 {
		t.Errorf("retryAfter = %#v, want 120", got)
	}
	if got := failure.Details["rateLimitReset"]; got != int64(1784810400) {
		t.Errorf("rateLimitReset = %#v, want int64(1784810400)", got)
	}
}

func TestClassifyHTTPErrorPreservesNonNumericRateLimitMetadata(t *testing.T) {
	t.Parallel()

	failure := classifyHTTPError(&api.HTTPError{
		StatusCode: http.StatusTooManyRequests,
		Headers: testHTTPHeaders(
			"Retry-After",
			"Wed, 23 Jul 2026 12:00:00 GMT",
			"X-RateLimit-Reset",
			"unknown",
		),
	}, "fallback")

	if got := failure.Details["retryAfter"]; got != "Wed, 23 Jul 2026 12:00:00 GMT" {
		t.Errorf("retryAfter = %#v", got)
	}
	if got := failure.Details["rateLimitReset"]; got != "unknown" {
		t.Errorf("rateLimitReset = %#v", got)
	}
}

func TestClassifyReadErrorHandlesCancellationAndExistingFailures(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := classifyReadError(ctx, errors.New("request stopped"), "fallback")
	if failure.Code != domain.CodeCancelled || !errors.Is(failure, context.Canceled) {
		t.Fatalf("cancelled-context failure = %v", failure)
	}

	timeoutFailure := classifyReadError(
		context.Background(),
		context.DeadlineExceeded,
		"fallback",
	)
	if timeoutFailure.Code != domain.CodeGitHubAPIError {
		t.Fatalf("live-context timeout code = %q, want github_api_error", timeoutFailure.Code)
	}

	existing := domain.NewFailure(
		domain.CodeTargetNotFound,
		"already classified",
		map[string]any{"target": "pull request"},
		errors.New("cause"),
	)
	if got := classifyReadError(context.Background(), existing, "fallback"); got != existing {
		t.Fatalf("existing failure pointer changed: got %p, want %p", got, existing)
	}
}

func testHTTPHeaders(namesAndValues ...string) http.Header {
	headers := make(http.Header, len(namesAndValues)/2)
	for index := 0; index < len(namesAndValues); index += 2 {
		headers.Set(namesAndValues[index], namesAndValues[index+1])
	}
	return headers
}
