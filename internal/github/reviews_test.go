package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pvzig/gh-suggest/internal/create"
	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestCreateReviewGroupsOrderedCommentsAndUsesExactPayload(t *testing.T) {
	t.Parallel()

	var captured requestSnapshot
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.GetBody != nil {
			t.Fatal("review POST is replayable through Request.GetBody")
		}
		captured = snapshotRequest(t, request)
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			`{
				"id": 99,
				"html_url": "https://github.com/octo/example/pull/42#pullrequestreview-99"
			}`,
		), nil
	}))

	created, err := adapter.CreateReview(context.Background(), validReviewRequest())
	if err != nil {
		t.Fatalf("CreateReview() error = %v", err)
	}
	if created.ID != 99 ||
		created.URL != "https://github.com/octo/example/pull/42#pullrequestreview-99" {
		t.Fatalf("created review = %#v", created)
	}
	if captured.Method != http.MethodPost ||
		captured.Path != "/repos/octo/example/pulls/42/reviews" {
		t.Fatalf("request = %s %s, want review POST", captured.Method, captured.Path)
	}

	var payload map[string]any
	if err := json.Unmarshal(captured.Body, &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v; body = %q", err, captured.Body)
	}
	if len(payload) != 4 {
		t.Fatalf("top-level payload keys = %#v, want exactly four", payload)
	}
	if payload["commit_id"] != strings.Repeat("b", 40) ||
		payload["body"] != "Grouped review summary." ||
		payload["event"] != domain.ReviewEventComment {
		t.Errorf("top-level payload = %#v", payload)
	}

	comments, ok := payload["comments"].([]any)
	if !ok || len(comments) != 2 {
		t.Fatalf("comments = %#v, want two ordered comments", payload["comments"])
	}
	first, ok := comments[0].(map[string]any)
	if !ok {
		t.Fatalf("first comment = %#v", comments[0])
	}
	if len(first) != 4 ||
		first["body"] != "First rendered suggestion." ||
		first["path"] != "Sources/First.swift" ||
		first["line"] != float64(7) ||
		first["side"] != string(domain.SideRight) {
		t.Errorf("first comment = %#v", first)
	}
	if _, present := first["start_line"]; present {
		t.Error("single-line comment unexpectedly contains start_line")
	}
	if _, present := first["start_side"]; present {
		t.Error("single-line comment unexpectedly contains start_side")
	}

	second, ok := comments[1].(map[string]any)
	if !ok {
		t.Fatalf("second comment = %#v", comments[1])
	}
	if len(second) != 6 ||
		second["body"] != "Second rendered suggestion." ||
		second["path"] != "Sources/Second.swift" ||
		second["start_line"] != float64(10) ||
		second["start_side"] != string(domain.SideRight) ||
		second["line"] != float64(12) ||
		second["side"] != string(domain.SideRight) {
		t.Errorf("second comment = %#v", second)
	}
}

func TestCreateReviewValidationHTTPFailureIsDefinitiveAndNotRetried(t *testing.T) {
	t.Parallel()

	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		return textResponse(
			request,
			http.StatusUnprocessableEntity,
			"application/json",
			`{"message":"Validation Failed"}`,
		), nil
	}))

	_, err := adapter.CreateReview(context.Background(), validReviewRequest())
	failure := assertFailure(t, err, domain.CodeGitHubAPIError)
	if failure.Code == domain.CodeWriteOutcomeUnknown {
		t.Fatal("HTTP validation response was incorrectly classified as ambiguous")
	}
	if got := failure.Details["status"]; got != http.StatusUnprocessableEntity {
		t.Fatalf("status = %#v, want %d", got, http.StatusUnprocessableEntity)
	}
	if got := failure.Details["githubMessage"]; got != "Validation Failed" {
		t.Fatalf("githubMessage = %#v, want Validation Failed", got)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}

func TestCreateReviewRejectsNonCommentEventBeforeAttempt(t *testing.T) {
	t.Parallel()

	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, errors.New("transport must not be called")
	}))
	request := validReviewRequest()
	request.Event = "APPROVE"

	_, err := adapter.CreateReview(context.Background(), request)
	failure := assertFailure(t, err, domain.CodeInvalidArguments)
	if failure.Details["event"] != "APPROVE" {
		t.Fatalf("failure details = %#v", failure.Details)
	}
	if requestCount != 0 {
		t.Fatalf("request count = %d, want 0", requestCount)
	}
}

func TestCreateReviewAmbiguousFailuresAreNotRetried(t *testing.T) {
	t.Parallel()

	transportFailure := errors.New("connection reset after write")
	tests := []struct {
		name      string
		roundTrip roundTripFunc
	}{
		{
			name: "transport error",
			roundTrip: func(request *http.Request) (*http.Response, error) {
				buffer := make([]byte, 1)
				_, _ = request.Body.Read(buffer)
				return nil, transportFailure
			},
		},
		{
			name: "success response cannot be decoded",
			roundTrip: func(request *http.Request) (*http.Response, error) {
				return textResponse(
					request,
					http.StatusOK,
					"application/json",
					`{"id":`,
				), nil
			},
		},
		{
			name: "success response omits id",
			roundTrip: func(request *http.Request) (*http.Response, error) {
				return textResponse(
					request,
					http.StatusOK,
					"application/json",
					`{"html_url":"https://github.com/octo/example/pull/42#pullrequestreview-99"}`,
				), nil
			},
		},
		{
			name: "success response omits url",
			roundTrip: func(request *http.Request) (*http.Response, error) {
				return textResponse(
					request,
					http.StatusOK,
					"application/json",
					`{"id":99}`,
				), nil
			},
		},
		{
			name: "unexpected success status",
			roundTrip: func(request *http.Request) (*http.Response, error) {
				return textResponse(
					request,
					http.StatusCreated,
					"application/json",
					`{
						"id":99,
						"html_url":"https://github.com/octo/example/pull/42#pullrequestreview-99"
					}`,
				), nil
			},
		},
		{
			name: "server error after possible creation",
			roundTrip: func(request *http.Request) (*http.Response, error) {
				return textResponse(
					request,
					http.StatusServiceUnavailable,
					"application/json",
					`{"message":"Service Unavailable"}`,
				), nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requestCount := 0
			adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestCount++
				return test.roundTrip(request)
			}))
			adapter.now = func() time.Time {
				return time.Date(2026, time.July, 23, 12, 34, 56, 789, time.UTC)
			}

			request := validReviewRequest()
			_, err := adapter.CreateReview(context.Background(), request)
			failure := assertFailure(t, err, domain.CodeWriteOutcomeUnknown)
			if requestCount != 1 {
				t.Fatalf("request count = %d, want 1", requestCount)
			}

			encodedDetails, encodeErr := json.Marshal(failure.Details)
			if encodeErr != nil {
				t.Fatalf("Marshal(failure details) error = %v", encodeErr)
			}
			details := string(encodedDetails)
			for _, secret := range []string{
				request.Body,
				request.Comments[0].Body,
				request.Comments[1].Body,
			} {
				if strings.Contains(details, secret) {
					t.Errorf("ambiguous failure details expose request body %q", secret)
				}
			}
		})
	}
}

func TestCreateReviewBoundsSuccessfulResponseBeforeDecoding(t *testing.T) {
	t.Parallel()

	requestCount := 0
	body := &observedReadCloser{
		reader: strings.NewReader(strings.Repeat("x", maxCreatedReviewResponseBytes+64)),
	}
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		// Avoid go-gh's JSON sanitizer buffering past the adapter's read boundary.
		return response(
			request,
			http.StatusOK,
			"application/octet-stream",
			body,
		), nil
	}))

	_, err := adapter.CreateReview(context.Background(), validReviewRequest())
	_ = assertFailure(t, err, domain.CodeWriteOutcomeUnknown)
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
	if body.bytesRead != maxCreatedReviewResponseBytes+1 {
		t.Fatalf(
			"response bytes read = %d, want %d",
			body.bytesRead,
			maxCreatedReviewResponseBytes+1,
		)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestCreateReviewBoundsErrorResponseWithoutLosingHTTPStatus(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		status   int
		wantCode domain.Code
	}{
		{name: "ambiguous server error", status: http.StatusServiceUnavailable, wantCode: domain.CodeWriteOutcomeUnknown},
		{name: "definitive validation error", status: http.StatusUnprocessableEntity, wantCode: domain.CodeGitHubAPIError},
		{name: "rate limit headers", status: http.StatusTooManyRequests, wantCode: domain.CodeRateLimited},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			body := &observedReadCloser{
				reader: strings.NewReader(`{"message":"` + strings.Repeat("x", maxErrorResponseBytes*3) + `"}`),
			}
			requestCount := 0
			adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestCount++
				_ = snapshotRequest(t, request)
				result := response(request, test.status, "application/json", body)
				result.Header.Set("Retry-After", "120")
				return result, nil
			}))
			_, err := adapter.CreateReview(context.Background(), validReviewRequest())
			failure := assertFailure(t, err, test.wantCode)
			if test.wantCode == domain.CodeWriteOutcomeUnknown {
				if failure.Details["reconciliation"] == nil {
					t.Fatal("ambiguous write lost its reconciliation descriptor")
				}
			} else if failure.Details["status"] != test.status {
				t.Fatalf("status = %v, want %d", failure.Details["status"], test.status)
			}
			if test.wantCode == domain.CodeRateLimited && failure.Details["retryAfter"] != 120 {
				t.Fatalf("rate-limit details = %#v", failure.Details)
			}
			// go-gh's streaming sanitizer has separate 4 KiB input and output
			// buffers that may both hold unread bytes from this ASCII fixture.
			if body.bytesRead < maxErrorResponseBytes+1 || body.bytesRead > maxErrorResponseBytes+8192 || !body.closed {
				t.Fatalf("read %d response bytes, closed = %t", body.bytesRead, body.closed)
			}
			if requestCount != 1 {
				t.Fatalf("request count = %d, want 1", requestCount)
			}
		})
	}
}

func TestCreateReviewCancellationBeforeAttemptIsDefinitive(t *testing.T) {
	t.Parallel()

	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, errors.New("transport must not be called")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.CreateReview(ctx, validReviewRequest())
	_ = assertFailure(t, err, domain.CodeCancelled)
	if requestCount != 0 {
		t.Fatalf("request count = %d, want 0", requestCount)
	}
}

func TestCreateReviewCancellationDuringAttemptIsAmbiguous(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		buffer := make([]byte, 1)
		_, _ = request.Body.Read(buffer)
		cancel()
		return nil, request.Context().Err()
	}))

	_, err := adapter.CreateReview(ctx, validReviewRequest())
	_ = assertFailure(t, err, domain.CodeWriteOutcomeUnknown)
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}

func TestCreateReviewConnectionSetupFailuresAreDefinitiveAndNotRetried(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "TLS handshake", err: errors.New("tls: handshake failure")},
		{name: "proxy connect", err: errors.New("proxyconnect tcp: connection refused")},
		{name: "dial timeout", err: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requestCount := 0
			adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestCount++
				if request.GetBody != nil {
					t.Fatal("review POST is replayable through Request.GetBody")
				}
				return nil, test.err
			}))

			_, err := adapter.CreateReview(context.Background(), validReviewRequest())
			failure := assertFailure(t, err, domain.CodeGitHubAPIError)
			if failure.Details["reason"] != "connection_failed_before_write" {
				t.Fatalf("details = %#v", failure.Details)
			}
			if requestCount != 1 {
				t.Fatalf("request count = %d, want 1", requestCount)
			}
		})
	}
}

func TestCreateReviewRedirectIsNotFollowedOrRetried(t *testing.T) {
	t.Parallel()

	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if requestCount > 1 {
			t.Fatalf("redirect caused request %d to %s", requestCount, request.URL)
		}
		redirect := textResponse(
			request,
			http.StatusTemporaryRedirect,
			"application/json",
			"",
		)
		redirect.Header.Set("Location", "https://example.invalid/captured")
		return redirect, nil
	}))

	_, err := adapter.CreateReview(context.Background(), validReviewRequest())
	_ = assertFailure(t, err, domain.CodeWriteOutcomeUnknown)
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
}

type observedReadCloser struct {
	reader    io.Reader
	bytesRead int
	closed    bool
}

func (body *observedReadCloser) Read(destination []byte) (int, error) {
	read, err := body.reader.Read(destination)
	body.bytesRead += read
	return read, err
}

func (body *observedReadCloser) Close() error {
	body.closed = true
	return nil
}

func validReviewRequest() create.ReviewRequest {
	startLine := 10
	return create.ReviewRequest{
		Ref:           testPullRequestRef(),
		BaseSHA:       strings.Repeat("a", 40),
		HeadSHA:       strings.Repeat("b", 40),
		Event:         domain.ReviewEventComment,
		Body:          "Grouped review summary.",
		RequestSHA256: strings.Repeat("c", 64),
		Comments: []create.ReviewComment{
			{
				Body:    "First rendered suggestion.",
				Path:    "Sources/First.swift",
				EndLine: 7,
			},
			{
				Body:      "Second rendered suggestion.",
				Path:      "Sources/Second.swift",
				StartLine: &startLine,
				EndLine:   12,
			},
		},
	}
}
