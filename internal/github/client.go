// Package github adapts authenticated go-gh clients to the application's
// endpoint-specific ports.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/pvzig/gh-suggest/internal/domain"
)

const (
	apiVersion = "2026-03-10"
	apiHost    = domain.SupportedHost
	apiBaseURL = "https://api.github.com/"
	timeout    = 30 * time.Second

	// listPageSize is the largest page GitHub serves for the list endpoints
	// this adapter reads.
	listPageSize = 100
	// maxListPages bounds sequential pagination. A target that needs more pages
	// than this is far broader than any suggestion workflow addresses, and
	// continuing would issue an unbounded number of requests.
	maxListPages = 100
)

type restClient interface {
	DoWithContext(
		ctx context.Context,
		method string,
		path string,
		body io.Reader,
		response any,
	) error
	RequestWithContext(
		ctx context.Context,
		method string,
		path string,
		body io.Reader,
	) (*http.Response, error)
}

// Adapter implements the GitHub-backed application ports. json and diff are
// separate clients so each request carries the endpoint's exact media type.
type Adapter struct {
	json restClient
	diff restClient
	now  func() time.Time
}

// NewAuthenticated creates clients from the authenticated GitHub CLI context.
// LogIgnoreEnv is always true so GH_DEBUG cannot disclose request bodies.
func NewAuthenticated() (*Adapter, error) {
	token, _ := auth.TokenForHost(apiHost)
	if token == "" {
		return nil, authenticationFailure(
			"No authenticated GitHub CLI token is available for github.com.",
			nil,
		)
	}

	return New(api.ClientOptions{AuthToken: token, Host: apiHost})
}

// New creates the adapter with explicit shared options. Transport is primarily
// useful for deterministic fake-server tests.
func New(options api.ClientOptions) (*Adapter, error) {
	if options.Host == "" {
		options.Host = apiHost
	}
	if !domain.IsSupportedHost(options.Host) {
		return nil, unsupportedHostFailure(options.Host)
	}
	options.Host = apiHost
	if options.Timeout == 0 {
		options.Timeout = timeout
	}
	options.LogIgnoreEnv = true
	jsonOptions := options
	jsonOptions.Headers = cloneHeaders(options.Headers)
	jsonOptions.Headers["Accept"] = "application/vnd.github+json"
	jsonOptions.Headers["X-GitHub-Api-Version"] = apiVersion

	diffOptions := options
	diffOptions.Headers = cloneHeaders(options.Headers)
	diffOptions.Headers["Accept"] = "application/vnd.github.diff"
	diffOptions.Headers["X-GitHub-Api-Version"] = apiVersion

	jsonClient, err := newRESTClient(jsonOptions)
	if err != nil {
		return nil, authenticationFailure("GitHub API client initialization failed.", err)
	}
	diffClient, err := newRESTClient(diffOptions)
	if err != nil {
		return nil, authenticationFailure("GitHub diff client initialization failed.", err)
	}

	return newAdapter(jsonClient, diffClient), nil
}

// httpRESTClient keeps go-gh's authentication, configuration, sanitizer, and
// optional Unix-socket routing while allowing this package to install its
// redirect guard around the fully resolved transport.
type httpRESTClient struct {
	client *http.Client
}

func newRESTClient(options api.ClientOptions) (*httpRESTClient, error) {
	client, err := api.NewHTTPClient(options)
	if err != nil {
		return nil, err
	}
	client.Transport = redirectRejectingTransport{base: client.Transport}
	return &httpRESTClient{client: client}, nil
}

func (client *httpRESTClient) DoWithContext(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
	response any,
) error {
	httpResponse, err := client.RequestWithContext(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer func() {
		_ = httpResponse.Body.Close()
	}()
	if httpResponse.StatusCode == http.StatusNoContent ||
		httpResponse.StatusCode == http.StatusResetContent {
		return nil
	}
	encoded, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, response)
}

func (client *httpRESTClient) RequestWithContext(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		apiBaseURL+strings.TrimPrefix(path, "/"),
		body,
	)
	if err != nil {
		return nil, err
	}
	httpResponse, err := client.client.Do(request)
	if err != nil {
		return nil, err
	}
	if httpResponse.StatusCode >= http.StatusOK &&
		httpResponse.StatusCode < http.StatusMultipleChoices {
		return httpResponse, nil
	}
	defer func() {
		_ = httpResponse.Body.Close()
	}()
	return nil, api.HandleHTTPError(httpResponse)
}

func newAdapter(jsonClient restClient, diffClient restClient) *Adapter {
	return &Adapter{
		json: jsonClient,
		diff: diffClient,
		now:  time.Now,
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	cloned := make(map[string]string, len(headers)+2)
	for name, value := range headers {
		if strings.EqualFold(name, "Accept") ||
			strings.EqualFold(name, "X-GitHub-Api-Version") {
			continue
		}
		cloned[http.CanonicalHeaderKey(name)] = value
	}
	return cloned
}

// redirectRejectingTransport stops redirects before net/http can replay a
// request body. GitHub suggestion creation has no idempotency key, so even a
// same-host 307 or 308 must remain a single, ambiguous POST attempt.
//
// Reads carry no such hazard and are allowed to follow redirects: a renamed or
// transferred repository answers 301, and replaying a GET is safe.
type redirectRejectingTransport struct {
	base http.RoundTripper
}

var errWriteRedirectRejected = errors.New("GitHub API write redirect rejected")

func (transport redirectRejectingTransport) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if replayableMethod(request.Method) {
		return response, nil
	}
	if response.StatusCode < http.StatusMultipleChoices ||
		response.StatusCode >= http.StatusBadRequest {
		return response, nil
	}

	if response.Body != nil {
		_ = response.Body.Close()
	}
	return nil, fmt.Errorf(
		"%w with status %d",
		errWriteRedirectRejected,
		response.StatusCode,
	)
}

// replayableMethod reports whether a request can be sent again without risking
// a duplicate external write.
func replayableMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func repositoryPath(owner string, repository string, suffix string) string {
	return fmt.Sprintf(
		"repos/%s/%s/%s",
		url.PathEscape(owner),
		url.PathEscape(repository),
		suffix,
	)
}
