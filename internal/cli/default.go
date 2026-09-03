package cli

import (
	"context"

	"github.com/pvzig/gh-suggest/internal/create"
	"github.com/pvzig/gh-suggest/internal/github"
	"github.com/pvzig/gh-suggest/internal/reconcile"
	"github.com/pvzig/gh-suggest/internal/resolve"
)

// DefaultCreator defers authentication and adapter wiring until a create
// request executes, allowing help and local parse failures to remain offline.
type DefaultCreator struct {
	repositories resolve.RepositoryContext
}

// NewDefaultCreator shares one current-repository resolution across preflight,
// local validation, and selector resolution.
func NewDefaultCreator() DefaultCreator {
	return DefaultCreator{repositories: newCachedRepositoryContext()}
}

// Preflight validates all create fields available before the replacement file
// is opened and resolves explicit/current host context without authentication.
func (creator DefaultCreator) Preflight(ctx context.Context, request create.Request) error {
	if failure := create.ValidateRequestFields(request); failure != nil {
		return failure
	}
	return resolve.ValidateLocalTarget(
		ctx,
		request.Selector,
		request.Repository,
		creator.repositoryContext(),
	)
}

// Execute validates the request locally, then authenticates and runs the
// create service. Authentication is deferred to here so parse failures and
// help stay offline.
func (creator DefaultCreator) Execute(
	ctx context.Context,
	request create.Request,
) (create.Result, error) {
	if failure := create.ValidateLocalRequest(request); failure != nil {
		return create.Result{}, failure
	}
	repositoryContext := creator.repositoryContext()
	if err := resolve.ValidateLocalTarget(
		ctx,
		request.Selector,
		request.Repository,
		repositoryContext,
	); err != nil {
		return create.Result{}, err
	}
	githubAdapter, err := github.NewAuthenticated()
	if err != nil {
		return create.Result{}, err
	}
	resolver := resolve.New(
		repositoryContext,
		githubAdapter,
		resolve.NewExecGitContext(),
	)
	service := create.NewService(
		resolver,
		githubAdapter,
		githubAdapter,
		githubAdapter,
	)
	return service.Execute(ctx, request)
}

func (creator DefaultCreator) repositoryContext() resolve.RepositoryContext {
	if creator.repositories != nil {
		return creator.repositories
	}
	return newCachedRepositoryContext()
}

// DefaultReconciler defers authentication and adapter wiring until a validated
// ambiguous-attempt descriptor is ready.
type DefaultReconciler struct{}

// NewDefaultReconciler creates the read-only whole-review reconciler.
func NewDefaultReconciler() DefaultReconciler {
	return DefaultReconciler{}
}

// Execute validates the request locally, then authenticates and runs the
// read-only reconciliation service.
func (reconciler DefaultReconciler) Execute(
	ctx context.Context,
	request reconcile.Request,
) (reconcile.Result, error) {
	if failure := reconcile.ValidateLocalRequest(request); failure != nil {
		return reconcile.Result{}, failure
	}
	githubAdapter, err := github.NewAuthenticated()
	if err != nil {
		return reconcile.Result{}, err
	}
	service := reconcile.NewService(githubAdapter, githubAdapter)
	return service.Execute(ctx, request)
}

func newCachedRepositoryContext() resolve.RepositoryContext {
	return resolve.NewCachedRepositoryContext(resolve.GoGHRepositoryContext{})
}
