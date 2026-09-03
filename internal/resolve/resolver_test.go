package resolve

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pvzig/gh-suggest/internal/domain"
)

type stubRepositoryContext struct {
	repository domain.Repository
	err        error
	calls      int
}

func (stub *stubRepositoryContext) CurrentRepository(context.Context) (domain.Repository, error) {
	stub.calls++
	return stub.repository, stub.err
}

type listCall struct {
	repository    domain.Repository
	qualifiedHead string
}

type stubPullRequestLister struct {
	candidates []Candidate
	err        error
	calls      []listCall
}

func (stub *stubPullRequestLister) ListOpenPullRequests(
	_ context.Context,
	repository domain.Repository,
	qualifiedHead string,
) ([]Candidate, error) {
	stub.calls = append(stub.calls, listCall{
		repository:    repository,
		qualifiedHead: qualifiedHead,
	})
	return append([]Candidate(nil), stub.candidates...), stub.err
}

type stubGitContext struct {
	branch Branch
	err    error
	calls  int
}

func (stub *stubGitContext) CurrentBranch(context.Context) (Branch, error) {
	stub.calls++
	return stub.branch, stub.err
}

func TestValidateLocalTargetPreservesPreAuthenticationPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("valid explicit target proceeds without repository context", func(t *testing.T) {
		t.Parallel()

		repositories := &stubRepositoryContext{err: errors.New("must not be called")}
		err := ValidateLocalTarget(
			context.Background(),
			"42",
			"octo-org/octo-repo",
			repositories,
		)
		if err != nil {
			t.Fatalf("ValidateLocalTarget() error = %v", err)
		}
		if repositories.calls != 0 {
			t.Fatalf("CurrentRepository() calls = %d, want 0", repositories.calls)
		}
	})

	t.Run("unsupported explicit host fails locally", func(t *testing.T) {
		t.Parallel()

		err := ValidateLocalTarget(
			context.Background(),
			"42",
			"enterprise.example/octo-org/octo-repo",
			nil,
		)
		_ = requireFailureCode(t, err, domain.CodeUnsupportedHost)
	})

	t.Run("unsupported current host fails locally", func(t *testing.T) {
		t.Parallel()

		repositories := &stubRepositoryContext{
			err: domain.NewFailure(
				domain.CodeUnsupportedHost,
				"Only github.com is supported.",
				nil,
				nil,
			),
		}
		err := ValidateLocalTarget(context.Background(), "42", "", repositories)
		_ = requireFailureCode(t, err, domain.CodeUnsupportedHost)
		if repositories.calls != 1 {
			t.Fatalf("CurrentRepository() calls = %d, want 1", repositories.calls)
		}
	})

	t.Run("current context errors fail locally", func(t *testing.T) {
		t.Parallel()

		repositories := &stubRepositoryContext{
			err: domain.NewFailure(
				domain.CodeIOError,
				"Current repository is unavailable.",
				nil,
				nil,
			),
		}
		err := ValidateLocalTarget(
			context.Background(),
			"42",
			"",
			repositories,
		)
		_ = requireFailureCode(t, err, domain.CodeIOError)
	})
}

func TestResolvePositiveNumber(t *testing.T) {
	t.Run("explicit repository", func(t *testing.T) {
		repositories := &stubRepositoryContext{}
		pullRequests := &stubPullRequestLister{}
		git := &stubGitContext{}
		resolver := New(repositories, pullRequests, git)

		ref, err := resolver.Resolve(context.Background(), "42", "octo-org/octo-repo")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		want := domain.PullRequestRef{
			Repository: domain.Repository{
				Host:  "github.com",
				Owner: "octo-org",
				Name:  "octo-repo",
			},
			Number: 42,
		}
		if !reflect.DeepEqual(ref, want) {
			t.Fatalf("Resolve() = %#v, want %#v", ref, want)
		}
		if repositories.calls != 0 || len(pullRequests.calls) != 0 || git.calls != 0 {
			t.Fatalf(
				"dependency calls = repositories:%d pullRequests:%d git:%d, want all zero",
				repositories.calls,
				len(pullRequests.calls),
				git.calls,
			)
		}
	})

	t.Run("current repository", func(t *testing.T) {
		repositories := &stubRepositoryContext{
			repository: domain.Repository{
				Host:  "GITHUB.COM",
				Owner: "octo-org",
				Name:  "octo-repo",
			},
		}
		resolver := New(repositories, &stubPullRequestLister{}, &stubGitContext{})

		ref, err := resolver.Resolve(context.Background(), "7", "")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if ref.Number != 7 || ref.Repository.Host != "github.com" {
			t.Fatalf("Resolve() = %#v, want pull request 7 on normalized github.com", ref)
		}
		if repositories.calls != 1 {
			t.Fatalf("CurrentRepository() calls = %d, want 1", repositories.calls)
		}
	})
}

func TestResolveRejectsNonPositiveAndOverflowedNumbers(t *testing.T) {
	for _, selector := range []string{"0", "-1", "+0", "+5", "-5", strings.Repeat("9", 100)} {
		t.Run(selector, func(t *testing.T) {
			pullRequests := &stubPullRequestLister{}
			resolver := New(
				&stubRepositoryContext{},
				pullRequests,
				&stubGitContext{},
			)

			_, err := resolver.Resolve(
				context.Background(),
				selector,
				"octo-org/octo-repo",
			)
			failure := requireFailureCode(t, err, domain.CodeInvalidArguments)
			if failure.Details["selector"] != selector {
				t.Fatalf("Details[selector] = %#v, want %q", failure.Details["selector"], selector)
			}
			if len(pullRequests.calls) != 0 {
				t.Fatalf("ListOpenPullRequests() calls = %d, want 0", len(pullRequests.calls))
			}
		})
	}
}

func TestResolvePullRequestURL(t *testing.T) {
	resolver := New(
		&stubRepositoryContext{err: errors.New("must not be called")},
		&stubPullRequestLister{err: errors.New("must not be called")},
		&stubGitContext{err: errors.New("must not be called")},
	)

	ref, err := resolver.Resolve(
		context.Background(),
		"https://GitHub.com/octo-org/octo-repo/pull/123",
		"",
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := domain.PullRequestRef{
		Repository: domain.Repository{
			Host:  "github.com",
			Owner: "octo-org",
			Name:  "octo-repo",
		},
		Number: 123,
	}
	if !reflect.DeepEqual(ref, want) {
		t.Fatalf("Resolve() = %#v, want %#v", ref, want)
	}
}

func TestResolvePullRequestURLRepositoryArgument(t *testing.T) {
	selector := "https://github.com/octo-org/octo-repo/pull/12"

	t.Run("matching repository is accepted case insensitively", func(t *testing.T) {
		resolver := New(nil, nil, nil)
		ref, err := resolver.Resolve(
			context.Background(),
			selector,
			"GITHUB.COM/OCTO-ORG/OCTO-REPO",
		)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if ref.Number != 12 {
			t.Fatalf("Number = %d, want 12", ref.Number)
		}
	})

	t.Run("conflicting repository is rejected", func(t *testing.T) {
		resolver := New(nil, nil, nil)
		_, err := resolver.Resolve(
			context.Background(),
			selector,
			"other-org/octo-repo",
		)
		failure := requireFailureCode(t, err, domain.CodeInvalidArguments)
		if failure.Details["repository"] != "other-org/octo-repo" {
			t.Fatalf(
				"Details[repository] = %#v, want other-org/octo-repo",
				failure.Details["repository"],
			)
		}
		if failure.Details["urlRepository"] != "octo-org/octo-repo" {
			t.Fatalf(
				"Details[urlRepository] = %#v, want octo-org/octo-repo",
				failure.Details["urlRepository"],
			)
		}
	})
}

func TestResolveRejectsInvalidPullRequestURLs(t *testing.T) {
	selectors := []string{
		"http://github.com/octo-org/octo-repo/pull/1",
		"https://user@github.com/octo-org/octo-repo/pull/1",
		"https://github.com:443/octo-org/octo-repo/pull/1",
		"https://github.com/octo-org/octo-repo/pull/0",
		"https://github.com/octo-org/octo-repo/pull/+1",
		"https://github.com/octo-org/octo-repo/pull/-1",
		"https://github.com/octo-org/octo-repo/pull/not-a-number",
		"https://github.com/octo-org/octo-repo/issues/1",
		"https://github.com/octo-org/octo-repo/pull/1/files",
		"https://github.com/octo-org/octo-repo/pull/1?diff=split",
		"https://github.com/octo-org/octo-repo/pull/1#discussion",
		"https://github.com/octo%2Forg/octo-repo/pull/1",
		"https://github.com/octo-org/repo%2Fother/pull/1",
	}
	for _, selector := range selectors {
		t.Run(selector, func(t *testing.T) {
			_, err := New(nil, nil, nil).Resolve(context.Background(), selector, "")
			_ = requireFailureCode(t, err, domain.CodeInvalidArguments)
		})
	}
}

func TestResolveRejectsUnsupportedURLHost(t *testing.T) {
	_, err := New(nil, nil, nil).Resolve(
		context.Background(),
		"https://git.example.com/octo-org/octo-repo/pull/1",
		"",
	)
	failure := requireFailureCode(t, err, domain.CodeUnsupportedHost)
	if failure.Details["host"] != "git.example.com" {
		t.Fatalf("Details[host] = %#v, want git.example.com", failure.Details["host"])
	}
}

func TestResolveQualifiedBranch(t *testing.T) {
	repository := domain.Repository{
		Host:  "github.com",
		Owner: "base-owner",
		Name:  "project",
	}
	pullRequests := &stubPullRequestLister{
		candidates: []Candidate{{
			Number:    31,
			HeadOwner: "fork-owner",
			HeadRef:   "feature",
		}},
	}
	resolver := New(&stubRepositoryContext{}, pullRequests, &stubGitContext{})

	ref, err := resolver.Resolve(
		context.Background(),
		"fork-owner:feature",
		"base-owner/project",
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if ref.Number != 31 || !reflect.DeepEqual(ref.Repository, repository) {
		t.Fatalf("Resolve() = %#v, want pull request 31 in %#v", ref, repository)
	}
	wantCalls := []listCall{{
		repository:    repository,
		qualifiedHead: "fork-owner:feature",
	}}
	if !reflect.DeepEqual(pullRequests.calls, wantCalls) {
		t.Fatalf("ListOpenPullRequests() calls = %#v, want %#v", pullRequests.calls, wantCalls)
	}
}

func TestCachedRepositoryContextResolvesOnce(t *testing.T) {
	inner := &stubRepositoryContext{
		repository: domain.Repository{Host: "github.com", Owner: "octo", Name: "example"},
	}
	cached := NewCachedRepositoryContext(inner)

	for attempt := range 3 {
		repository, err := cached.CurrentRepository(context.Background())
		if err != nil {
			t.Fatalf("CurrentRepository() attempt %d error = %v", attempt, err)
		}
		if repository != inner.repository {
			t.Fatalf("CurrentRepository() = %#v, want %#v", repository, inner.repository)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("inner CurrentRepository() calls = %d, want 1", inner.calls)
	}
}

func TestCachedRepositoryContextCachesFailures(t *testing.T) {
	inner := &stubRepositoryContext{err: errors.New("no remote")}
	cached := NewCachedRepositoryContext(inner)

	for attempt := range 2 {
		if _, err := cached.CurrentRepository(context.Background()); err == nil {
			t.Fatalf("CurrentRepository() attempt %d error = nil, want the cached failure", attempt)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("inner CurrentRepository() calls = %d, want 1", inner.calls)
	}
}

func TestCachedRepositoryContextDoesNotCacheCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "context error", err: context.Canceled},
		{name: "deadline error", err: context.DeadlineExceeded},
		{
			name: "cancelled failure",
			err: domain.NewFailure(
				domain.CodeCancelled,
				"Reading the current Git branch was cancelled.",
				nil,
				context.Canceled,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := domain.Repository{Host: "github.com", Owner: "octo", Name: "example"}
			inner := &stubRepositoryContext{err: test.err}
			cached := NewCachedRepositoryContext(inner)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := cached.CurrentRepository(ctx); err == nil {
				t.Fatal("CurrentRepository() error = nil, want the cancellation")
			}

			// A later call with a live context must be able to resolve, rather
			// than inherit the interrupted call's error.
			inner.err = nil
			inner.repository = repository
			got, err := cached.CurrentRepository(context.Background())
			if err != nil {
				t.Fatalf("CurrentRepository() after cancellation error = %v, want a fresh resolution", err)
			}
			if got != repository {
				t.Fatalf("CurrentRepository() = %#v, want %#v", got, repository)
			}
			if inner.calls != 2 {
				t.Fatalf("inner CurrentRepository() calls = %d, want 2", inner.calls)
			}
		})
	}
}

func TestResolveQualifiedBranchRefiltersUntrustedServerResults(t *testing.T) {
	for _, test := range []struct {
		name      string
		candidate Candidate
	}{
		{
			name:      "different head owner",
			candidate: Candidate{Number: 31, HeadOwner: "other-owner", HeadRef: "feature"},
		},
		{
			name:      "different head ref",
			candidate: Candidate{Number: 31, HeadOwner: "fork-owner", HeadRef: "other"},
		},
		{
			name:      "unknown head repository",
			candidate: Candidate{Number: 31, HeadRef: "feature"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pullRequests := &stubPullRequestLister{candidates: []Candidate{test.candidate}}
			_, err := New(
				&stubRepositoryContext{},
				pullRequests,
				&stubGitContext{},
			).Resolve(context.Background(), "fork-owner:feature", "base-owner/project")
			failure := requireFailureCode(t, err, domain.CodeTargetNotFound)
			if failure.Details["selector"] != "fork-owner:feature" {
				t.Fatalf("Details[selector] = %#v, want fork-owner:feature", failure.Details["selector"])
			}
		})
	}
}

func TestResolveRejectsMalformedQualifiedBranch(t *testing.T) {
	for _, selector := range []string{":feature", "fork-owner:"} {
		t.Run(selector, func(t *testing.T) {
			pullRequests := &stubPullRequestLister{}
			_, err := New(
				&stubRepositoryContext{},
				pullRequests,
				&stubGitContext{},
			).Resolve(context.Background(), selector, "base-owner/project")
			_ = requireFailureCode(t, err, domain.CodeInvalidArguments)
			if len(pullRequests.calls) != 0 {
				t.Fatalf("ListOpenPullRequests() calls = %d, want 0", len(pullRequests.calls))
			}
		})
	}
}

func TestResolveUnqualifiedBranchFiltersEveryReturnedCandidate(t *testing.T) {
	candidates := make([]Candidate, 100)
	for index := range candidates {
		candidates[index] = Candidate{
			Number:    index + 1,
			HeadOwner: "someone",
			HeadRef:   "other",
		}
	}
	candidates = append(candidates, Candidate{
		Number:    101,
		HeadOwner: "fork-owner",
		HeadRef:   "feature",
	})
	pullRequests := &stubPullRequestLister{candidates: candidates}
	resolver := New(&stubRepositoryContext{}, pullRequests, &stubGitContext{})

	ref, err := resolver.Resolve(
		context.Background(),
		"feature",
		"base-owner/project",
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if ref.Number != 101 {
		t.Fatalf("Number = %d, want 101 from the aggregate paginated result", ref.Number)
	}
	if len(pullRequests.calls) != 1 {
		t.Fatalf("ListOpenPullRequests() calls = %d, want 1", len(pullRequests.calls))
	}
	if pullRequests.calls[0].qualifiedHead != "" {
		t.Fatalf(
			"qualifiedHead = %q, want empty so the lister paginates all open pull requests",
			pullRequests.calls[0].qualifiedHead,
		)
	}
}

func TestResolveUnqualifiedBranchIsCaseSensitive(t *testing.T) {
	pullRequests := &stubPullRequestLister{
		candidates: []Candidate{
			{Number: 1, HeadRef: "Feature"},
			{Number: 2, HeadRef: "feature"},
		},
	}
	ref, err := New(
		&stubRepositoryContext{},
		pullRequests,
		&stubGitContext{},
	).Resolve(context.Background(), "feature", "base-owner/project")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if ref.Number != 2 {
		t.Fatalf("Number = %d, want 2", ref.Number)
	}
}

func TestResolveBranchRequiresUniqueCandidate(t *testing.T) {
	for _, test := range []struct {
		name       string
		candidates []Candidate
		code       domain.Code
		matches    any
	}{
		{
			name: "missing",
			code: domain.CodeTargetNotFound,
		},
		{
			name: "ambiguous",
			candidates: []Candidate{
				{Number: 1, HeadOwner: "first", HeadRef: "feature"},
				{Number: 2, HeadOwner: "second", HeadRef: "feature"},
			},
			code:    domain.CodeInvalidArguments,
			matches: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pullRequests := &stubPullRequestLister{candidates: test.candidates}
			_, err := New(
				&stubRepositoryContext{},
				pullRequests,
				&stubGitContext{},
			).Resolve(context.Background(), "feature", "base-owner/project")
			failure := requireFailureCode(t, err, test.code)
			if failure.Details["selector"] != "feature" {
				t.Fatalf("Details[selector] = %#v, want feature", failure.Details["selector"])
			}
			if test.matches != nil && failure.Details["matches"] != test.matches {
				t.Fatalf("Details[matches] = %#v, want %#v", failure.Details["matches"], test.matches)
			}
		})
	}
}

func TestResolveBranchPropagatesListerFailure(t *testing.T) {
	want := domain.NewFailure(domain.CodeRateLimited, "rate limited", nil, nil)
	_, err := New(
		&stubRepositoryContext{},
		&stubPullRequestLister{err: want},
		&stubGitContext{},
	).Resolve(context.Background(), "feature", "base-owner/project")
	if err != want {
		t.Fatalf("Resolve() error = %v, want original failure %v", err, want)
	}
}

func TestResolveCurrentBranch(t *testing.T) {
	t.Run("fork owner qualifies the API filter", func(t *testing.T) {
		pullRequests := &stubPullRequestLister{
			candidates: []Candidate{{
				Number:    8,
				HeadOwner: "fork-owner",
				HeadRef:   "feature",
			}},
		}
		git := &stubGitContext{
			branch: Branch{Name: "feature", RemoteOwner: "fork-owner"},
		}
		ref, err := New(
			&stubRepositoryContext{
				repository: domain.Repository{
					Host:  "github.com",
					Owner: "base-owner",
					Name:  "project",
				},
			},
			pullRequests,
			git,
		).Resolve(context.Background(), "", "")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if ref.Number != 8 {
			t.Fatalf("Number = %d, want 8", ref.Number)
		}
		if len(pullRequests.calls) != 1 ||
			pullRequests.calls[0].qualifiedHead != "fork-owner:feature" {
			t.Fatalf("ListOpenPullRequests() calls = %#v, want qualified fork head", pullRequests.calls)
		}
		if git.calls != 1 {
			t.Fatalf("CurrentBranch() calls = %d, want 1", git.calls)
		}
	})

	t.Run("branch without remote owner uses the aggregate list", func(t *testing.T) {
		pullRequests := &stubPullRequestLister{
			candidates: []Candidate{{Number: 9, HeadRef: "feature"}},
		}
		ref, err := New(
			&stubRepositoryContext{
				repository: domain.Repository{
					Host:  "github.com",
					Owner: "base-owner",
					Name:  "project",
				},
			},
			pullRequests,
			&stubGitContext{branch: Branch{Name: "feature"}},
		).Resolve(context.Background(), "", "")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if ref.Number != 9 {
			t.Fatalf("Number = %d, want 9", ref.Number)
		}
		if len(pullRequests.calls) != 1 || pullRequests.calls[0].qualifiedHead != "" {
			t.Fatalf("ListOpenPullRequests() calls = %#v, want unqualified list", pullRequests.calls)
		}
	})
}

func TestResolveCurrentBranchRejectsDetachedHead(t *testing.T) {
	t.Run("Git context failure", func(t *testing.T) {
		detached := detachedHeadFailure(errors.New("detached"))
		_, err := New(
			&stubRepositoryContext{
				repository: domain.Repository{
					Host:  "github.com",
					Owner: "base-owner",
					Name:  "project",
				},
			},
			&stubPullRequestLister{},
			&stubGitContext{err: detached},
		).Resolve(context.Background(), "", "")
		if err != detached {
			t.Fatalf("Resolve() error = %v, want original detached-head failure", err)
		}
	})

	t.Run("empty successful branch result", func(t *testing.T) {
		pullRequests := &stubPullRequestLister{}
		_, err := New(
			&stubRepositoryContext{
				repository: domain.Repository{
					Host:  "github.com",
					Owner: "base-owner",
					Name:  "project",
				},
			},
			pullRequests,
			&stubGitContext{},
		).Resolve(context.Background(), "", "")
		failure := requireFailureCode(t, err, domain.CodeIOError)
		if failure.Details["reason"] != "detached_head" {
			t.Fatalf("Details[reason] = %#v, want detached_head", failure.Details["reason"])
		}
		if len(pullRequests.calls) != 0 {
			t.Fatalf("ListOpenPullRequests() calls = %d, want 0", len(pullRequests.calls))
		}
	})
}

func TestParseRepository(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  domain.Repository
	}{
		{
			name:  "owner and repository",
			value: "octo-org/octo-repo",
			want: domain.Repository{
				Host:  "github.com",
				Owner: "octo-org",
				Name:  "octo-repo",
			},
		},
		{
			name:  "explicit host",
			value: "GITHUB.COM/octo-org/octo-repo",
			want: domain.Repository{
				Host:  "github.com",
				Owner: "octo-org",
				Name:  "octo-repo",
			},
		},
		{
			name:  "repository punctuation",
			value: "octo-org/octo.repo_v1",
			want: domain.Repository{
				Host:  "github.com",
				Owner: "octo-org",
				Name:  "octo.repo_v1",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseRepository(test.value)
			if err != nil {
				t.Fatalf("ParseRepository() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ParseRepository() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseRepositoryRejectsInvalidValues(t *testing.T) {
	values := []string{
		"",
		"owner",
		"/owner/repository",
		"owner/repository/",
		"owner//repository",
		"owner/repository/extra/path",
		"owner/repo?query",
		"owner/repo#fragment",
		"owner/../repository",
		" owner/repository",
		"owner/repository ",
		"own!er/repository",
		"-owner/repository",
		"owner-/repository",
		"own--er/repository",
		"owner/repo%2Fother",
		"owner/repo:other",
		"owner/repo name",
		"owner/" + strings.Repeat("r", 101),
		strings.Repeat("o", 40) + "/repository",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			_, err := ParseRepository(value)
			_ = requireFailureCode(t, err, domain.CodeInvalidArguments)
		})
	}
}

func TestRepositoryResolutionRejectsUnsupportedHosts(t *testing.T) {
	t.Run("explicit repository", func(t *testing.T) {
		_, err := New(nil, nil, nil).Resolve(
			context.Background(),
			"1",
			"git.example.com/owner/repository",
		)
		failure := requireFailureCode(t, err, domain.CodeUnsupportedHost)
		if failure.Details["host"] != "git.example.com" {
			t.Fatalf("Details[host] = %#v, want git.example.com", failure.Details["host"])
		}
	})

	t.Run("current repository", func(t *testing.T) {
		_, err := New(
			&stubRepositoryContext{
				repository: domain.Repository{
					Host:  "git.example.com",
					Owner: "owner",
					Name:  "repository",
				},
			},
			nil,
			nil,
		).Resolve(context.Background(), "1", "")
		_ = requireFailureCode(t, err, domain.CodeUnsupportedHost)
	})
}

func TestRepositoryResolutionClassifiesAndPreservesFailures(t *testing.T) {
	t.Run("unknown current repository error", func(t *testing.T) {
		cause := errors.New("not a Git repository")
		_, err := New(
			&stubRepositoryContext{err: cause},
			nil,
			nil,
		).Resolve(context.Background(), "1", "")
		failure := requireFailureCode(t, err, domain.CodeIOError)
		if !errors.Is(failure, cause) {
			t.Fatalf("Resolve() error = %v, want cause %v", failure, cause)
		}
	})

	t.Run("stable current repository failure", func(t *testing.T) {
		want := domain.NewFailure(domain.CodeCancelled, "cancelled", nil, context.Canceled)
		_, err := New(
			&stubRepositoryContext{err: want},
			nil,
			nil,
		).Resolve(context.Background(), "1", "")
		if err != want {
			t.Fatalf("Resolve() error = %v, want original failure %v", err, want)
		}
	})
}

func requireFailureCode(t *testing.T, err error, code domain.Code) *domain.Failure {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", code)
	}
	failure, ok := domain.AsFailure(err)
	if !ok {
		t.Fatalf("error type = %T, want *domain.Failure", err)
	}
	if failure.Code != code {
		t.Fatalf("failure.Code = %q, want %q (error = %v)", failure.Code, code, failure)
	}
	return failure
}
