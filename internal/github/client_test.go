package github

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/pvzig/gh-suggest/internal/domain"
)

func TestNewAppliesExactHeadersForEveryEndpoint(t *testing.T) {
	t.Parallel()

	var requests []requestSnapshot
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, snapshotRequest(t, request))

		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/octo/example/pulls/42" &&
			request.Header.Get("Accept") == "application/vnd.github.diff":
			return textResponse(request, http.StatusOK, "text/plain", "diff"), nil
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/octo/example/pulls/42":
			return textResponse(
				request,
				http.StatusOK,
				"application/json",
				`{
					"number": 42,
					"html_url": "https://github.com/octo/example/pull/42",
					"state": "open",
					"changed_files": 1,
					"base": {"sha": "base"},
					"head": {
						"sha": "head",
						"ref": "feature",
						"repo": {"owner": {"login": "fork-owner"}}
					}
				}`,
			), nil
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/octo/example/pulls":
			return textResponse(request, http.StatusOK, "application/json", "[]"), nil
		case request.Method == http.MethodPost &&
			request.URL.Path == "/repos/octo/example/pulls/42/reviews":
			return textResponse(
				request,
				http.StatusOK,
				"application/json",
				`{"id": 99, "html_url": "https://github.com/octo/example/pull/42#pullrequestreview-99"}`,
			), nil
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/octo/example/pulls/42/reviews":
			return textResponse(request, http.StatusOK, "application/json", "[]"), nil
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/octo/example/pulls/42/reviews/99/comments":
			return textResponse(request, http.StatusOK, "application/json", "[]"), nil
		default:
			t.Fatalf("unexpected request: %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			return nil, nil
		}
	})

	headers := map[string]string{
		"accept":                   "application/untrusted",
		"x-github-api-version":     "2000-01-01",
		"X-Test-Caller-Preserved":  "preserved",
		"x-test-canonicalized-key": "canonicalized",
	}
	adapter, err := New(api.ClientOptions{
		AuthToken: "test-token",
		Headers:   headers,
		Host:      apiHost,
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ref := testPullRequestRef()
	if _, err := adapter.ReadPullRequest(context.Background(), ref); err != nil {
		t.Fatalf("ReadPullRequest() error = %v", err)
	}
	if _, err := adapter.ListOpenPullRequests(
		context.Background(),
		ref.Repository,
		"fork-owner:feature",
	); err != nil {
		t.Fatalf("ListOpenPullRequests() error = %v", err)
	}
	if _, err := adapter.ReadDiff(context.Background(), ref); err != nil {
		t.Fatalf("ReadDiff() error = %v", err)
	}
	if _, err := adapter.ListReviews(
		context.Background(),
		ref,
		time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("ListReviews() error = %v", err)
	}
	if _, err := adapter.ListReviewComments(context.Background(), ref, 99); err != nil {
		t.Fatalf("ListReviewComments() error = %v", err)
	}
	if _, err := adapter.CreateReview(context.Background(), validReviewRequest()); err != nil {
		t.Fatalf("CreateReview() error = %v", err)
	}

	if len(requests) != 6 {
		t.Fatalf("request count = %d, want 6", len(requests))
	}
	for index, request := range requests {
		wantAccept := "application/vnd.github+json"
		if request.Path == "/repos/octo/example/pulls/42" &&
			request.Method == http.MethodGet &&
			index == 2 {
			wantAccept = "application/vnd.github.diff"
		}
		if got := request.Headers.Values("Accept"); len(got) != 1 || got[0] != wantAccept {
			t.Errorf("request %d Accept values = %q, want [%q]", index, got, wantAccept)
		}
		if got := request.Headers.Values("X-GitHub-Api-Version"); len(got) != 1 ||
			got[0] != apiVersion {
			t.Errorf(
				"request %d X-GitHub-Api-Version values = %q, want [%q]",
				index,
				got,
				apiVersion,
			)
		}
		if got := request.Headers.Get("X-Test-Caller-Preserved"); got != "preserved" {
			t.Errorf("request %d caller header = %q, want preserved", index, got)
		}
		if got := request.Headers.Get("X-Test-Canonicalized-Key"); got != "canonicalized" {
			t.Errorf("request %d canonicalized header = %q, want canonicalized", index, got)
		}
	}
	if headers["accept"] != "application/untrusted" ||
		headers["x-github-api-version"] != "2000-01-01" {
		t.Fatalf("New() mutated caller headers: %#v", headers)
	}
}

func TestNewSuppressesAmbientGHDebug(t *testing.T) {
	t.Setenv("GH_DEBUG", "api")

	previousStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stderr = writer
	t.Cleanup(func() {
		os.Stderr = previousStderr
		_ = writer.Close()
		_ = reader.Close()
	})

	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return textResponse(
			request,
			http.StatusOK,
			"application/json",
			`{"id": 99, "html_url": "https://github.com/octo/example/pull/42#pullrequestreview-99"}`,
		), nil
	})
	adapter := newHTTPAdapter(t, transport)
	request := validReviewRequest()
	request.Comments[0].Body = "```suggestion\nsecret replacement\n```"
	_, err = adapter.CreateReview(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateReview() error = %v", err)
	}

	os.Stderr = previousStderr
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll(stderr) error = %v", err)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("GH_DEBUG output = %q, want empty", output)
	}
}

func TestNewPreservesUnixSocketRouting(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "ghs-")
	if err != nil {
		t.Fatalf("os.MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socketPath := filepath.Join(directory, "github.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/octo/example/pulls/42" {
			t.Errorf("request path = %q", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"number": 42,
			"html_url": "https://github.com/octo/example/pull/42",
			"state": "open",
			"changed_files": 1,
			"base": {"sha": "base"},
			"head": {"sha": "head", "ref": "feature", "repo": {"owner": {"login": "octo"}}}
		}`)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close() })

	adapter, err := New(api.ClientOptions{
		AuthToken:        "test-token",
		Host:             apiHost,
		UnixDomainSocket: socketPath,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	pullRequest, err := adapter.ReadPullRequest(context.Background(), testPullRequestRef())
	if err != nil {
		t.Fatalf("ReadPullRequest() error = %v", err)
	}
	if pullRequest.Ref.Number != 42 {
		t.Fatalf("pull request = %#v", pullRequest)
	}
}

func TestNewAuthenticatedReportsMissingToken(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", t.TempDir()+"/missing-gh")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	_, err := NewAuthenticated()
	_ = assertFailure(t, err, domain.CodeAuthenticationFailed)
}

func TestNewRejectsUnsupportedHost(t *testing.T) {
	t.Parallel()

	_, err := New(api.ClientOptions{
		AuthToken: "test-token",
		Host:      "enterprise.example",
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("transport must not be called")
			return nil, nil
		}),
	})
	failure := assertFailure(t, err, domain.CodeUnsupportedHost)
	if got := failure.Details["host"]; got != "enterprise.example" {
		t.Fatalf("failure host = %#v, want enterprise.example", got)
	}
}
