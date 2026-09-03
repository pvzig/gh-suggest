package github

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/pvzig/gh-suggest/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type requestSnapshot struct {
	Method  string
	Path    string
	Query   string
	Headers http.Header
	Body    []byte
}

func snapshotRequest(t *testing.T, request *http.Request) requestSnapshot {
	t.Helper()

	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("ReadAll(request.Body) error = %v", err)
		}
	}
	return requestSnapshot{
		Method:  request.Method,
		Path:    request.URL.Path,
		Query:   request.URL.RawQuery,
		Headers: request.Header.Clone(),
		Body:    bytes.Clone(body),
	}
}

func newHTTPAdapter(t *testing.T, transport http.RoundTripper) *Adapter {
	t.Helper()

	adapter, err := New(api.ClientOptions{
		AuthToken: "test-token",
		Host:      apiHost,
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return adapter
}

func newPaginatedJSONAdapter(
	t *testing.T,
	expectedPath string,
	pageBodies ...string,
) (*Adapter, func() int) {
	t.Helper()

	requestCount := 0
	adapter := newHTTPAdapter(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if request.Method != http.MethodGet || request.URL.Path != expectedPath {
			t.Fatalf("request = %s %s, want GET %s", request.Method, request.URL.Path, expectedPath)
		}
		query := request.URL.Query()
		if query.Get("per_page") != "100" || query.Get("page") != fmt.Sprint(requestCount) {
			t.Fatalf("pagination query = %q for request %d", request.URL.RawQuery, requestCount)
		}
		if requestCount > len(pageBodies) {
			t.Fatalf("unexpected pagination request %d", requestCount)
			return nil, nil
		}
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			pageBodies[requestCount-1],
		), nil
	}))
	return adapter, func() int { return requestCount }
}

func response(
	request *http.Request,
	statusCode int,
	contentType string,
	body io.ReadCloser,
) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     header,
		Body:       body,
		Request:    request,
	}
}

func textResponse(
	request *http.Request,
	statusCode int,
	contentType string,
	body string,
) *http.Response {
	return response(
		request,
		statusCode,
		contentType,
		io.NopCloser(bytes.NewBufferString(body)),
	)
}

func assertFailure(
	t *testing.T,
	err error,
	wantCode domain.Code,
) *domain.Failure {
	t.Helper()

	failure, ok := domain.AsFailure(err)
	if !ok {
		t.Fatalf("error = %T %v, want *domain.Failure", err, err)
	}
	if failure.Code != wantCode {
		t.Fatalf("failure.Code = %q, want %q (failure = %#v)", failure.Code, wantCode, failure)
	}
	return failure
}

func testPullRequestRef() domain.PullRequestRef {
	return domain.PullRequestRef{
		Repository: domain.Repository{
			Host:  apiHost,
			Owner: "octo",
			Name:  "example",
		},
		Number: 42,
	}
}
