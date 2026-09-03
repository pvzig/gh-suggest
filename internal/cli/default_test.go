package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pvzig/gh-suggest/internal/attempt"
	"github.com/pvzig/gh-suggest/internal/create"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/reconcile"
	"github.com/pvzig/gh-suggest/internal/resolve"
)

func TestDefaultCreatorPreservesLocalAndHostFailurePrecedence(t *testing.T) {
	configureMissingAuthentication(t)

	tests := []struct {
		name     string
		mutate   func(*create.Request)
		wantCode domain.Code
	}{
		{
			name: "invalid local path",
			mutate: func(request *create.Request) {
				request.Suggestions[0].Path = "../outside.go"
			},
			wantCode: domain.CodeInvalidArguments,
		},
		{
			name: "direct write needs no validation receipt",
			mutate: func(request *create.Request) {
				request.DryRun = false
			},
			wantCode: domain.CodeAuthenticationFailed,
		},
		{
			name: "invalid optional head",
			mutate: func(request *create.Request) {
				request.ExpectedHeadSHA = "short"
			},
			wantCode: domain.CodeInvalidArguments,
		},
		{
			name: "unsupported explicit host",
			mutate: func(request *create.Request) {
				request.Repository = "enterprise.example/octo/example"
			},
			wantCode: domain.CodeUnsupportedHost,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validDefaultCreateRequest()
			test.mutate(&request)
			_, err := (DefaultCreator{}).Execute(context.Background(), request)
			assertDefaultFailureCode(t, err, test.wantCode)
		})
	}
}

func TestDefaultReconcilerValidatesBeforeAuthentication(t *testing.T) {
	configureMissingAuthentication(t)

	request := reconcile.Request{Descriptor: validDefaultDescriptor()}
	request.Descriptor.Repository = "invalid"
	_, err := (DefaultReconciler{}).Execute(context.Background(), request)
	assertDefaultFailureCode(t, err, domain.CodeInvalidArguments)

	request.Descriptor = validDefaultDescriptor()
	request.Descriptor.Host = "enterprise.example"
	_, err = (DefaultReconciler{}).Execute(context.Background(), request)
	assertDefaultFailureCode(t, err, domain.CodeUnsupportedHost)
}

func TestDefaultCreatorResolvesCurrentRepositoryOnce(t *testing.T) {
	configureMissingAuthentication(t)

	repositories := &countingRepositoryContext{
		repository: domain.Repository{
			Host:  "github.com",
			Owner: "octo",
			Name:  "example",
		},
	}
	creator := DefaultCreator{
		repositories: resolve.NewCachedRepositoryContext(repositories),
	}
	request := validDefaultCreateRequest()
	request.Repository = ""

	if err := creator.Preflight(context.Background(), request); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if _, err := creator.Execute(context.Background(), request); err == nil {
		t.Fatal("Execute() error = nil, want missing authentication")
	}
	if repositories.calls != 1 {
		t.Fatalf("CurrentRepository() calls = %d, want 1", repositories.calls)
	}
}

type countingRepositoryContext struct {
	repository domain.Repository
	calls      int
}

func (context_ *countingRepositoryContext) CurrentRepository(
	context.Context,
) (domain.Repository, error) {
	context_.calls++
	return context_.repository, nil
}

func validDefaultCreateRequest() create.Request {
	return create.Request{
		Selector:   "42",
		Repository: "octo/example",
		ReviewBody: "Focused review.",
		Suggestions: []create.Suggestion{
			{
				Path:        "first.go",
				EndLine:     7,
				Replacement: []byte("replacement"),
			},
		},
		DryRun: true,
	}
}

func validDefaultDescriptor() attempt.Descriptor {
	return attempt.Descriptor{
		SchemaVersion:    attempt.DescriptorSchemaVersion,
		Host:             "github.com",
		Repository:       "octo/example",
		PullRequest:      42,
		BaseSHA:          strings.Repeat("a", 40),
		HeadSHA:          strings.Repeat("b", 40),
		Event:            domain.ReviewEventComment,
		ReviewBodySHA256: strings.Repeat("c", 64),
		Suggestions: []attempt.Suggestion{
			{
				Path:       "first.go",
				EndLine:    7,
				Side:       string(domain.SideRight),
				BodySHA256: strings.Repeat("d", 64),
			},
		},
		RequestSHA256:    strings.Repeat("e", 64),
		AttemptStartedAt: time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC),
	}
}

func configureMissingAuthentication(t *testing.T) {
	t.Helper()
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", t.TempDir()+"/missing-gh")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
}

func assertDefaultFailureCode(t *testing.T, err error, want domain.Code) {
	t.Helper()
	failure, ok := domain.AsFailure(err)
	if !ok {
		t.Fatalf("error = %T %v, want *domain.Failure", err, err)
	}
	if failure.Code != want {
		t.Fatalf("failure code = %q, want %q", failure.Code, want)
	}
}
