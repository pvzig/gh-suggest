package resolve

import (
	"context"
	"sync"

	ghrepository "github.com/cli/go-gh/v2/pkg/repository"

	"github.com/pvzig/gh-suggest/internal/domain"
)

// GoGHRepositoryContext applies normal GitHub CLI repository resolution.
type GoGHRepositoryContext struct{}

// CurrentRepository resolves the repository for the working directory. The
// underlying GitHub CLI helper takes no context, so cancellation is observed by
// callers rather than here.
func (GoGHRepositoryContext) CurrentRepository(context.Context) (domain.Repository, error) {
	repository, err := ghrepository.Current()
	if err != nil {
		return domain.Repository{}, domain.NewFailure(
			domain.CodeIOError,
			"The current GitHub repository could not be resolved.",
			nil,
			err,
		)
	}
	return validateRepository(domain.Repository{
		Host:  repository.Host,
		Owner: repository.Owner,
		Name:  repository.Name,
	})
}

// CachedRepositoryContext resolves the current repository at most once. The
// underlying resolution forks Git and reads GitHub CLI configuration, and a
// process cannot change its working directory mid-run, so one command shares a
// single authoritative answer instead of re-reading it per validation step.
//
// A cancelled resolution is deliberately not cached: it describes the caller's
// context rather than the repository, and reusing it would fail every later
// call that still has a live context.
type CachedRepositoryContext struct {
	inner      RepositoryContext
	mutex      sync.Mutex
	resolved   bool
	repository domain.Repository
	err        error
}

// NewCachedRepositoryContext wraps inner with single-resolution caching.
func NewCachedRepositoryContext(inner RepositoryContext) *CachedRepositoryContext {
	return &CachedRepositoryContext{inner: inner}
}

// CurrentRepository returns the cached resolution, performing it on first use.
func (cached *CachedRepositoryContext) CurrentRepository(
	ctx context.Context,
) (domain.Repository, error) {
	cached.mutex.Lock()
	defer cached.mutex.Unlock()

	if cached.resolved {
		return cached.repository, cached.err
	}

	repository, err := cached.inner.CurrentRepository(ctx)
	if domain.IsCancellation(ctx, err) {
		return domain.Repository{}, err
	}
	cached.resolved = true
	cached.repository = repository
	cached.err = err
	return repository, err
}
