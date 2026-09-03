// Package resolve implements explicit pull-request selector semantics.
package resolve

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/pvzig/gh-suggest/internal/domain"
)

// Candidate contains the fields needed to resolve a branch selector.
type Candidate struct {
	Number    int
	HeadOwner string
	HeadRef   string
}

// RepositoryContext reports the repository the command is running against when
// no explicit --repo argument was given.
type RepositoryContext interface {
	CurrentRepository(ctx context.Context) (domain.Repository, error)
}

// PullRequestLister returns the complete result across every API page. An empty
// qualifiedHead requests all open pull requests for local exact-ref filtering.
type PullRequestLister interface {
	ListOpenPullRequests(
		ctx context.Context,
		repository domain.Repository,
		qualifiedHead string,
	) ([]Candidate, error)
}

// Branch is the checked-out branch and, when it can be determined, the owner
// of the GitHub remote it pushes to.
type Branch struct {
	Name        string
	RemoteOwner string
}

// GitContext reads local Git state.
type GitContext interface {
	CurrentBranch(ctx context.Context) (Branch, error)
}

// Resolver turns a selector into the address of exactly one pull request.
type Resolver struct {
	repositories RepositoryContext
	pullRequests PullRequestLister
	git          GitContext
}

// New wires the ports selector resolution depends on.
func New(
	repositories RepositoryContext,
	pullRequests PullRequestLister,
	git GitContext,
) *Resolver {
	return &Resolver{
		repositories: repositories,
		pullRequests: pullRequests,
		git:          git,
	}
}

// ValidateLocalTarget applies selector and explicit repository syntax,
// conflict, and host rules without authenticating or making an API request. If
// the target relies on current-repository context, local resolution failures
// are returned before replacement-file reads or authentication.
func ValidateLocalTarget(
	ctx context.Context,
	selector string,
	repositoryArgument string,
	repositories RepositoryContext,
) error {
	if _, handled, err := explicitURLTarget(selector, repositoryArgument); handled || err != nil {
		return err
	}
	if repositoryArgument != "" {
		if _, err := ParseRepository(repositoryArgument); err != nil {
			return err
		}
	}
	if _, numeric, err := parseNumber(selector); numeric && err != nil {
		return err
	}
	if owner, branch, qualified := strings.Cut(selector, ":"); qualified &&
		(owner == "" || branch == "") {
		return invalidSelector(selector)
	}

	if repositoryArgument != "" || repositories == nil {
		return nil
	}
	_, err := repositories.CurrentRepository(ctx)
	return err
}

// Resolve returns the pull request addressed by selector. An empty selector
// uses the current branch, which must match exactly one open pull request.
func (resolver *Resolver) Resolve(
	ctx context.Context,
	selector string,
	repositoryArgument string,
) (domain.PullRequestRef, error) {
	if parsedURL, handled, err := explicitURLTarget(
		selector,
		repositoryArgument,
	); handled || err != nil {
		if err != nil {
			return domain.PullRequestRef{}, err
		}
		return parsedURL, nil
	}

	repository, err := resolver.resolveRepository(ctx, repositoryArgument)
	if err != nil {
		return domain.PullRequestRef{}, err
	}

	if number, numeric, parseErr := parseNumber(selector); numeric {
		if parseErr != nil {
			return domain.PullRequestRef{}, parseErr
		}
		return domain.PullRequestRef{Repository: repository, Number: number}, nil
	}

	if selector == "" {
		branch, branchErr := resolver.git.CurrentBranch(ctx)
		if branchErr != nil {
			return domain.PullRequestRef{}, branchErr
		}
		if branch.Name == "" {
			return domain.PullRequestRef{}, detachedHeadFailure(nil)
		}
		if branch.RemoteOwner != "" {
			return resolver.resolveQualifiedBranch(
				ctx,
				repository,
				branch.RemoteOwner+":"+branch.Name,
			)
		}
		return resolver.resolveUnqualifiedBranch(ctx, repository, branch.Name)
	}
	if owner, branch, qualified := strings.Cut(selector, ":"); qualified {
		if owner == "" || branch == "" {
			return domain.PullRequestRef{}, invalidSelector(selector)
		}
		return resolver.resolveQualifiedBranch(ctx, repository, owner+":"+branch)
	}
	return resolver.resolveUnqualifiedBranch(ctx, repository, selector)
}

func explicitURLTarget(
	selector string,
	repositoryArgument string,
) (domain.PullRequestRef, bool, error) {
	parsedURL, handled, err := parsePullRequestURL(selector)
	if !handled || err != nil {
		return parsedURL, handled, err
	}
	if repositoryArgument == "" {
		return parsedURL, true, nil
	}

	explicitRepository, err := ParseRepository(repositoryArgument)
	if err != nil {
		return domain.PullRequestRef{}, true, err
	}
	if !repositoriesEqual(explicitRepository, parsedURL.Repository) {
		return domain.PullRequestRef{}, true, domain.NewFailure(
			domain.CodeInvalidArguments,
			"The pull-request URL conflicts with --repo.",
			map[string]any{
				"repository":    explicitRepository.String(),
				"urlRepository": parsedURL.Repository.String(),
			},
			nil,
		)
	}
	return parsedURL, true, nil
}

func (resolver *Resolver) resolveRepository(
	ctx context.Context,
	argument string,
) (domain.Repository, error) {
	if argument != "" {
		return ParseRepository(argument)
	}
	repository, err := resolver.repositories.CurrentRepository(ctx)
	if err != nil {
		if failure, ok := domain.AsFailure(err); ok {
			return domain.Repository{}, failure
		}
		return domain.Repository{}, domain.NewFailure(
			domain.CodeIOError,
			"The current GitHub repository could not be resolved.",
			nil,
			err,
		)
	}
	return validateRepository(repository)
}

// resolveQualifiedBranch asks GitHub for the exact OWNER:BRANCH match and then
// re-applies the same filter locally. A server-side filter that is ignored
// degrades to every open pull request, which must never silently resolve to an
// unrelated write target.
func (resolver *Resolver) resolveQualifiedBranch(
	ctx context.Context,
	repository domain.Repository,
	head string,
) (domain.PullRequestRef, error) {
	owner, branch, qualified := strings.Cut(head, ":")
	if !qualified || owner == "" || branch == "" {
		return domain.PullRequestRef{}, invalidSelector(head)
	}
	candidates, err := resolver.pullRequests.ListOpenPullRequests(ctx, repository, head)
	if err != nil {
		return domain.PullRequestRef{}, err
	}
	matches := make([]Candidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.HeadRef == branch &&
			strings.EqualFold(candidate.HeadOwner, owner) {
			matches = append(matches, candidate)
		}
	}
	return uniqueCandidate(repository, head, matches)
}

func (resolver *Resolver) resolveUnqualifiedBranch(
	ctx context.Context,
	repository domain.Repository,
	branch string,
) (domain.PullRequestRef, error) {
	if branch == "" {
		return domain.PullRequestRef{}, invalidSelector(branch)
	}
	candidates, err := resolver.pullRequests.ListOpenPullRequests(ctx, repository, "")
	if err != nil {
		return domain.PullRequestRef{}, err
	}
	matches := make([]Candidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.HeadRef == branch {
			matches = append(matches, candidate)
		}
	}
	return uniqueCandidate(repository, branch, matches)
}

func uniqueCandidate(
	repository domain.Repository,
	selector string,
	candidates []Candidate,
) (domain.PullRequestRef, error) {
	switch len(candidates) {
	case 0:
		return domain.PullRequestRef{}, domain.NewFailure(
			domain.CodeTargetNotFound,
			"No open pull request uniquely matches the selector.",
			map[string]any{"selector": selector},
			nil,
		)
	case 1:
		return domain.PullRequestRef{
			Repository: repository,
			Number:     candidates[0].Number,
		}, nil
	default:
		return domain.PullRequestRef{}, domain.NewFailure(
			domain.CodeInvalidArguments,
			"The pull-request selector is ambiguous.",
			map[string]any{
				"selector": selector,
				"matches":  len(candidates),
			},
			nil,
		)
	}
}

// ParseRepository parses an OWNER/REPOSITORY or HOST/OWNER/REPOSITORY argument
// and validates the result against the supported host.
func ParseRepository(value string) (domain.Repository, error) {
	parts := strings.Split(value, "/")
	var repository domain.Repository
	switch len(parts) {
	case 2:
		repository = domain.Repository{
			Host:  domain.SupportedHost,
			Owner: parts[0],
			Name:  parts[1],
		}
	case 3:
		if parts[0] == "" {
			return domain.Repository{}, invalidRepositoryArgument(value)
		}
		repository = domain.Repository{Host: strings.ToLower(parts[0]), Owner: parts[1], Name: parts[2]}
	default:
		return domain.Repository{}, invalidRepositoryArgument(value)
	}
	return validateRepository(repository)
}

func validateRepository(repository domain.Repository) (domain.Repository, error) {
	if repository.Owner == "" || repository.Name == "" {
		return domain.Repository{}, domain.NewFailure(
			domain.CodeInvalidArguments,
			"The repository owner and name must not be empty.",
			nil,
			nil,
		)
	}
	if !domain.ValidRepositoryOwner(repository.Owner) ||
		!domain.ValidRepositoryName(repository.Name) {
		return domain.Repository{}, domain.NewFailure(
			domain.CodeInvalidArguments,
			"The repository owner and name contain invalid characters.",
			nil,
			nil,
		)
	}
	if repository.Host == "" {
		repository.Host = domain.SupportedHost
	}
	if !domain.IsSupportedHost(repository.Host) {
		return domain.Repository{}, domain.NewFailure(
			domain.CodeUnsupportedHost,
			"Only github.com is supported.",
			map[string]any{"host": repository.Host},
			nil,
		)
	}
	repository.Host = domain.SupportedHost
	return repository, nil
}

func parsePullRequestURL(selector string) (domain.PullRequestRef, bool, error) {
	if !strings.Contains(selector, "://") {
		return domain.PullRequestRef{}, false, nil
	}
	parsed, err := url.Parse(selector)
	if err != nil {
		return domain.PullRequestRef{}, true, invalidSelector(selector)
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" {
		return domain.PullRequestRef{}, true, invalidSelector(selector)
	}
	if !domain.IsSupportedHost(parsed.Hostname()) {
		return domain.PullRequestRef{}, true, domain.NewFailure(
			domain.CodeUnsupportedHost,
			"Only github.com pull-request URLs are supported.",
			map[string]any{"host": parsed.Hostname()},
			nil,
		)
	}
	if parsed.User != nil || parsed.Port() != "" {
		return domain.PullRequestRef{}, true, invalidSelector(selector)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return domain.PullRequestRef{}, true, invalidSelector(selector)
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return domain.PullRequestRef{}, true, invalidSelector(selector)
	}
	owner, ownerErr := url.PathUnescape(parts[0])
	name, nameErr := url.PathUnescape(parts[1])
	if ownerErr != nil || nameErr != nil {
		return domain.PullRequestRef{}, true, invalidSelector(selector)
	}
	number, numeric, numberErr := parseNumber(parts[3])
	if !numeric || numberErr != nil {
		return domain.PullRequestRef{}, true, invalidSelector(selector)
	}
	repository, repositoryErr := validateRepository(domain.Repository{
		Host:  domain.SupportedHost,
		Owner: owner,
		Name:  name,
	})
	if repositoryErr != nil {
		return domain.PullRequestRef{}, true, repositoryErr
	}
	return domain.PullRequestRef{Repository: repository, Number: number}, true, nil
}

func parseNumber(selector string) (int, bool, error) {
	if selector == "" {
		return 0, false, nil
	}
	if !decimalIntegerSyntax(selector) {
		return 0, false, nil
	}
	// A signed selector looks like a pull-request number but is not one, so it
	// is rejected instead of resolving to the unsigned number.
	if selector[0] == '+' || selector[0] == '-' {
		return 0, true, invalidSelector(selector)
	}
	number, err := strconv.Atoi(selector)
	if err != nil || number <= 0 {
		return 0, true, invalidSelector(selector)
	}
	return number, true, nil
}

func decimalIntegerSyntax(value string) bool {
	if value == "" {
		return false
	}
	start := 0
	if value[0] == '+' || value[0] == '-' {
		start = 1
	}
	if start == len(value) {
		return false
	}
	for index := start; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func invalidSelector(selector string) *domain.Failure {
	return domain.NewFailure(
		domain.CodeInvalidArguments,
		"The pull-request selector is invalid.",
		map[string]any{"selector": selector},
		nil,
	)
}

func repositoriesEqual(first domain.Repository, second domain.Repository) bool {
	return strings.EqualFold(first.Host, second.Host) &&
		strings.EqualFold(first.Owner, second.Owner) &&
		strings.EqualFold(first.Name, second.Name)
}

func invalidRepositoryArgument(value string) *domain.Failure {
	return domain.NewFailure(
		domain.CodeInvalidArguments,
		"--repo must be OWNER/REPOSITORY or HOST/OWNER/REPOSITORY.",
		map[string]any{"repository": value},
		nil,
	)
}
