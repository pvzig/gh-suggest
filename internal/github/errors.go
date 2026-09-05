package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/pvzig/gh-suggest/internal/domain"
)

const maxErrorResponseBytes = 1 << 20

// boundedHTTPError retains the received status and headers even when error
// diagnostics overflow. In particular, a POST's 5xx must remain ambiguous.
// The caller still owns and closes the original response body.
func boundedHTTPError(response *http.Response) error {
	limited := &io.LimitedReader{R: response.Body, N: maxErrorResponseBytes + 1}
	bounded := *response
	bounded.Body = io.NopCloser(limited)
	err := api.HandleHTTPError(&bounded)
	if limited.N == 0 {
		return &api.HTTPError{
			StatusCode: response.StatusCode,
			Headers:    response.Header,
			RequestURL: response.Request.URL,
			Message:    fmt.Sprintf("GitHub error response exceeds the %d-byte limit", maxErrorResponseBytes),
		}
	}
	return err
}

func authenticationFailure(message string, cause error) *domain.Failure {
	return domain.NewFailure(domain.CodeAuthenticationFailed, message, nil, cause)
}

func unsupportedHostFailure(host string) *domain.Failure {
	return domain.NewFailure(
		domain.CodeUnsupportedHost,
		"Only github.com is supported.",
		map[string]any{"host": host},
		nil,
	)
}

func classifyReadError(ctx context.Context, err error, message string) *domain.Failure {
	if failure, ok := domain.AsFailure(err); ok {
		return failure
	}
	if failure := domain.ContextCancellationFailure(
		ctx,
		err,
		"The GitHub request was cancelled.",
	); failure != nil {
		return failure
	}

	httpError, ok := errors.AsType[*api.HTTPError](err)
	if !ok {
		return domain.NewFailure(domain.CodeGitHubAPIError, message, nil, err)
	}

	return classifyHTTPError(httpError, message)
}

func classifyHTTPError(httpError *api.HTTPError, fallbackMessage string) *domain.Failure {
	details := map[string]any{"status": httpError.StatusCode}
	switch {
	case httpError.StatusCode == http.StatusUnauthorized ||
		httpError.Headers.Get("WWW-Authenticate") != "":
		return domain.NewFailure(
			domain.CodeAuthenticationFailed,
			"GitHub rejected authentication.",
			details,
			httpError,
		)
	case isRateLimitError(httpError):
		addRateLimitDetails(details, httpError.Headers)
		return domain.NewFailure(
			domain.CodeRateLimited,
			"GitHub rate limited the request.",
			details,
			httpError,
		)
	case httpError.StatusCode == http.StatusForbidden:
		return domain.NewFailure(
			domain.CodePermissionDenied,
			"GitHub authentication succeeded, but permission was denied.",
			details,
			httpError,
		)
	case httpError.StatusCode == http.StatusNotFound:
		return domain.NewFailure(
			domain.CodeTargetNotFound,
			"The GitHub target was not found or is inaccessible.",
			details,
			httpError,
		)
	default:
		if httpError.Message != "" {
			details["githubMessage"] = httpError.Message
		}
		return domain.NewFailure(
			domain.CodeGitHubAPIError,
			fallbackMessage,
			details,
			httpError,
		)
	}
}

func isRateLimitError(httpError *api.HTTPError) bool {
	if httpError.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if httpError.StatusCode != http.StatusForbidden {
		return false
	}
	if httpError.Headers.Get("X-RateLimit-Remaining") == "0" ||
		httpError.Headers.Get("Retry-After") != "" {
		return true
	}
	message := strings.ToLower(httpError.Message)
	return strings.Contains(message, "rate limit") ||
		strings.Contains(message, "secondary rate") ||
		strings.Contains(message, "abuse detection")
}

func addRateLimitDetails(details map[string]any, headers http.Header) {
	if retryAfter := headers.Get("Retry-After"); retryAfter != "" {
		if seconds, err := strconv.Atoi(retryAfter); err == nil {
			details["retryAfter"] = seconds
		} else {
			details["retryAfter"] = retryAfter
		}
	}
	if reset := headers.Get("X-RateLimit-Reset"); reset != "" {
		if timestamp, err := strconv.ParseInt(reset, 10, 64); err == nil {
			details["rateLimitReset"] = timestamp
		} else {
			details["rateLimitReset"] = reset
		}
	}
}
