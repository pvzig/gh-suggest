package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	pulldiff "github.com/pvzig/gh-suggest/internal/diff"
	"github.com/pvzig/gh-suggest/internal/domain"
)

type trackingBody struct {
	reader io.Reader
	closed bool
	reads  int
}

func (body *trackingBody) Read(buffer []byte) (int, error) {
	body.reads++
	return body.reader.Read(buffer)
}

func (body *trackingBody) Close() error {
	body.closed = true
	return nil
}

type failingReader struct {
	err error
}

func (reader failingReader) Read([]byte) (int, error) {
	return 0, reader.err
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func TestReadDiffReturnsRawBodyAndClosesIt(t *testing.T) {
	t.Parallel()

	body := &trackingBody{reader: strings.NewReader("raw diff contents")}
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet ||
			request.URL.Path != "/repos/octo/example/pulls/42" {
			t.Fatalf("request = %s %s, want raw diff GET", request.Method, request.URL.Path)
		}
		return response(request, http.StatusOK, "text/plain", body), nil
	}))

	content, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
	if err != nil {
		t.Fatalf("ReadDiff() error = %v", err)
	}
	if got := string(content); got != "raw diff contents" {
		t.Fatalf("content = %q, want raw diff contents", got)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestReadDiffRejectsDeclaredOversizeWithoutReadingAndCloses(t *testing.T) {
	t.Parallel()

	body := &trackingBody{reader: strings.NewReader("must not be read")}
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		result := response(request, http.StatusOK, "text/plain", body)
		result.ContentLength = pulldiff.MaxDiffBytes + 1
		return result, nil
	}))

	_, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
	failure := assertFailure(t, err, domain.CodeRangeNotCommentable)
	if got := failure.Details["reason"]; got != "diff_too_large" {
		t.Fatalf("reason = %#v, want diff_too_large", got)
	}
	if got := failure.Details["contentLength"]; got != pulldiff.MaxDiffBytes+1 {
		t.Fatalf("contentLength = %#v, want %d", got, pulldiff.MaxDiffBytes+1)
	}
	if got := failure.Details["maximumBytes"]; got != pulldiff.MaxDiffBytes {
		t.Fatalf("maximumBytes = %#v, want %d", got, pulldiff.MaxDiffBytes)
	}
	if body.reads != 0 {
		t.Fatalf("body reads = %d, want 0", body.reads)
	}
	if !body.closed {
		t.Fatal("oversized response body was not closed")
	}
}

func TestReadDiffRejectsDeclaredLimitWithoutReading(t *testing.T) {
	t.Parallel()

	body := &trackingBody{reader: strings.NewReader("must not be read")}
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		result := response(request, http.StatusOK, "text/plain", body)
		result.ContentLength = pulldiff.MaxDiffBytes
		return result, nil
	}))

	_, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
	failure := assertFailure(t, err, domain.CodeRangeNotCommentable)
	if got := failure.Details["reason"]; got != "diff_too_large" {
		t.Fatalf("reason = %#v, want diff_too_large", got)
	}
	if body.reads != 0 {
		t.Fatalf("body reads = %d, want 0", body.reads)
	}
}

func TestReadDiffRejectsStreamingBodyOverLimitAndCloses(t *testing.T) {
	t.Parallel()

	body := &trackingBody{
		reader: io.LimitReader(zeroReader{}, pulldiff.MaxDiffBytes+1),
	}
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		result := response(request, http.StatusOK, "text/plain", body)
		result.ContentLength = -1
		return result, nil
	}))

	_, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
	failure := assertFailure(t, err, domain.CodeRangeNotCommentable)
	if got := failure.Details["reason"]; got != "diff_too_large" {
		t.Fatalf("reason = %#v, want diff_too_large", got)
	}
	if got := failure.Details["maximumBytes"]; got != pulldiff.MaxDiffBytes {
		t.Fatalf("maximumBytes = %#v, want %d", got, pulldiff.MaxDiffBytes)
	}
	if !body.closed {
		t.Fatal("streaming oversized response body was not closed")
	}
}

func TestReadDiffReadFailureIsUnavailableAndCloses(t *testing.T) {
	t.Parallel()

	readError := errors.New("connection ended mid-body")
	body := &trackingBody{reader: failingReader{err: readError}}
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusOK, "text/plain", body), nil
	}))

	_, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
	failure := assertFailure(t, err, domain.CodeRangeNotCommentable)
	if got := failure.Details["reason"]; got != "diff_unavailable" {
		t.Fatalf("reason = %#v, want diff_unavailable", got)
	}
	if !errors.Is(failure, readError) {
		t.Fatalf("failure = %v, want wrapped read error", failure)
	}
	if !body.closed {
		t.Fatal("failed response body was not closed")
	}
}

func TestReadDiffPreservesCancellationDuringBodyRead(t *testing.T) {
	t.Parallel()

	for _, readError := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(readError.Error(), func(t *testing.T) {
			t.Parallel()

			body := &trackingBody{reader: failingReader{err: readError}}
			adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return response(request, http.StatusOK, "text/plain", body), nil
			}))

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := adapter.ReadDiff(ctx, testPullRequestRef())
			failure := assertFailure(t, err, domain.CodeCancelled)
			if !errors.Is(failure, readError) {
				t.Fatalf("failure = %v, want wrapped cancellation", failure)
			}
			if !body.closed {
				t.Fatal("cancelled response body was not closed")
			}
		})
	}
}

func TestReadDiffTreatsTransportDeadlineAsReadFailureWhileContextIsLive(t *testing.T) {
	t.Parallel()

	body := &trackingBody{reader: failingReader{err: context.DeadlineExceeded}}
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusOK, "text/plain", body), nil
	}))
	_, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
	_ = assertFailure(t, err, domain.CodeRangeNotCommentable)
}

func TestReadDiffHTTPFailureIsUnavailableAndCloses(t *testing.T) {
	t.Parallel()

	body := &trackingBody{reader: strings.NewReader(`{"message":"server failure"}`)}
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(
			request,
			http.StatusInternalServerError,
			"application/json",
			body,
		), nil
	}))

	_, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
	failure := assertFailure(t, err, domain.CodeRangeNotCommentable)
	if got := failure.Details["reason"]; got != "diff_unavailable" {
		t.Fatalf("reason = %#v, want diff_unavailable", got)
	}
	if got := failure.Details["status"]; got != http.StatusInternalServerError {
		t.Fatalf("status = %#v, want %d", got, http.StatusInternalServerError)
	}
	if !body.closed {
		t.Fatal("HTTP error response body was not closed")
	}
}

func TestReadDiffPreservesHigherPriorityHTTPClassifications(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		headers  http.Header
		wantCode domain.Code
	}{
		{
			name:     "authentication",
			status:   http.StatusUnauthorized,
			wantCode: domain.CodeAuthenticationFailed,
		},
		{
			name:     "permission",
			status:   http.StatusForbidden,
			wantCode: domain.CodePermissionDenied,
		},
		{
			name:   "rate limit",
			status: http.StatusForbidden,
			headers: http.Header{
				http.CanonicalHeaderKey("X-RateLimit-Remaining"): []string{"0"},
			},
			wantCode: domain.CodeRateLimited,
		},
		{
			name:     "not found",
			status:   http.StatusNotFound,
			wantCode: domain.CodeTargetNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				result := response(
					request,
					test.status,
					"application/json",
					io.NopCloser(strings.NewReader(`{"message":"failure"}`)),
				)
				result.Header = make(http.Header)
				for name, values := range test.headers {
					result.Header[name] = append([]string(nil), values...)
				}
				result.Header.Set("Content-Type", "application/json")
				return result, nil
			}))

			_, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
			_ = assertFailure(t, err, test.wantCode)
		})
	}
}

func TestReadDiffTransportFailureIsNotRetried(t *testing.T) {
	t.Parallel()

	transportError := errors.New("network unavailable")
	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, transportError
	}))

	_, err := adapter.ReadDiff(context.Background(), testPullRequestRef())
	failure := assertFailure(t, err, domain.CodeRangeNotCommentable)
	if got := failure.Details["reason"]; got != "diff_unavailable" {
		t.Fatalf("reason = %#v, want diff_unavailable", got)
	}
	if !errors.Is(failure, transportError) {
		t.Fatalf("failure = %v, want wrapped transport error", failure)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}
