package resolve

import (
	"context"
	"net/url"
	"os/exec"
	"strings"

	"github.com/pvzig/gh-suggest/internal/domain"
)

type commandRunner interface {
	Run(ctx context.Context, name string, arguments ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, arguments...).Output()
}

// ExecGitContext reads only the current branch and its configured remote.
type ExecGitContext struct {
	runner commandRunner
}

// NewExecGitContext returns a GitContext backed by the git executable.
func NewExecGitContext() *ExecGitContext {
	return &ExecGitContext{runner: execRunner{}}
}

// CurrentBranch reports the checked-out branch and the owner of the GitHub
// remote it pushes to. A detached HEAD is a failure; an unreadable or non-GitHub
// remote simply leaves the owner empty.
func (git *ExecGitContext) CurrentBranch(ctx context.Context) (Branch, error) {
	output, err := git.runner.Run(ctx, "git", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if cancellationErr := gitCancellationFailure(ctx, err); cancellationErr != nil {
			return Branch{}, cancellationErr
		}
		return Branch{}, detachedHeadFailure(err)
	}
	branchName := strings.TrimSpace(string(output))
	if branchName == "" {
		return Branch{}, detachedHeadFailure(nil)
	}

	remoteName, remoteErr := git.remoteName(ctx, branchName)
	if remoteErr != nil {
		return Branch{}, remoteErr
	}
	if remoteName == "" {
		return Branch{Name: branchName}, nil
	}
	remoteURL, err := git.runner.Run(ctx, "git", "remote", "get-url", "--push", remoteName)
	if err != nil {
		if cancellationErr := gitCancellationFailure(ctx, err); cancellationErr != nil {
			return Branch{}, cancellationErr
		}
		return Branch{Name: branchName}, nil
	}

	return Branch{
		Name:        branchName,
		RemoteOwner: githubRemoteOwner(strings.TrimSpace(string(remoteURL))),
	}, nil
}

func (git *ExecGitContext) remoteName(ctx context.Context, branchName string) (string, error) {
	keys := []string{
		"branch." + branchName + ".pushRemote",
		"remote.pushDefault",
		"branch." + branchName + ".remote",
	}
	for _, key := range keys {
		value, err := git.configValue(ctx, key)
		if err != nil {
			return "", err
		}
		if value == "." {
			return "", nil
		}
		if value != "" {
			return value, nil
		}
	}
	return "origin", nil
}

func (git *ExecGitContext) configValue(ctx context.Context, key string) (string, error) {
	output, err := git.runner.Run(ctx, "git", "config", "--get", key)
	if err != nil {
		if cancellationErr := gitCancellationFailure(ctx, err); cancellationErr != nil {
			return "", cancellationErr
		}
		return "", nil
	}
	return strings.TrimSpace(string(output)), nil
}

func githubRemoteOwner(remote string) string {
	remote = strings.TrimSpace(remote)
	parsed, err := url.Parse(remote)
	if err == nil && parsed.Host != "" {
		if !domain.IsSupportedHost(parsed.Hostname()) {
			return ""
		}
		return ownerFromRemotePath(parsed.Path)
	}

	separator := strings.IndexByte(remote, ':')
	if separator <= 0 {
		return ""
	}
	host := remote[:separator]
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if !domain.IsSupportedHost(host) {
		return ""
	}
	return ownerFromRemotePath(remote[separator+1:])
}

func ownerFromRemotePath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0]
}

func gitCancellationFailure(ctx context.Context, err error) *domain.Failure {
	return domain.ContextCancellationFailure(
		ctx,
		err,
		"Reading the current Git branch was cancelled.",
	)
}

func detachedHeadFailure(cause error) *domain.Failure {
	return domain.NewFailure(
		domain.CodeIOError,
		"The current checkout is detached or its Git branch cannot be read.",
		map[string]any{"reason": "detached_head"},
		cause,
	)
}
