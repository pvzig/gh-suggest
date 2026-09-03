package resolve

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pvzig/gh-suggest/internal/domain"
)

type expectedCommand struct {
	name      string
	arguments []string
	output    string
	err       error
}

type scriptedRunner struct {
	t            *testing.T
	commands     []expectedCommand
	next         int
	beforeReturn func(expectedCommand)
}

func (runner *scriptedRunner) Run(
	_ context.Context,
	name string,
	arguments ...string,
) ([]byte, error) {
	runner.t.Helper()
	if runner.next >= len(runner.commands) {
		runner.t.Fatalf("unexpected command: %s %v", name, arguments)
	}
	command := runner.commands[runner.next]
	runner.next++
	if name != command.name || !reflect.DeepEqual(arguments, command.arguments) {
		runner.t.Fatalf(
			"command %d = %s %v, want %s %v",
			runner.next,
			name,
			arguments,
			command.name,
			command.arguments,
		)
	}
	if runner.beforeReturn != nil {
		runner.beforeReturn(command)
	}
	return []byte(command.output), command.err
}

func (runner *scriptedRunner) requireDone() {
	runner.t.Helper()
	if runner.next != len(runner.commands) {
		runner.t.Fatalf(
			"executed %d commands, want %d; next expected command = %#v",
			runner.next,
			len(runner.commands),
			runner.commands[runner.next],
		)
	}
}

func TestExecGitContextUsesPushRemoteAndPushURLPrecedence(t *testing.T) {
	missing := errors.New("config value not found")
	for _, test := range []struct {
		name       string
		config     []expectedCommand
		remoteName string
	}{
		{
			name: "branch push remote",
			config: []expectedCommand{
				gitConfigCommand("branch.feature.pushRemote", "fork\n", nil),
			},
			remoteName: "fork",
		},
		{
			name: "default push remote before tracking remote",
			config: []expectedCommand{
				gitConfigCommand("branch.feature.pushRemote", "", missing),
				gitConfigCommand("remote.pushDefault", "publish\n", nil),
			},
			remoteName: "publish",
		},
		{
			name: "tracking remote",
			config: []expectedCommand{
				gitConfigCommand("branch.feature.pushRemote", "", missing),
				gitConfigCommand("remote.pushDefault", "", missing),
				gitConfigCommand("branch.feature.remote", "upstream\n", nil),
			},
			remoteName: "upstream",
		},
		{
			name: "origin fallback",
			config: []expectedCommand{
				gitConfigCommand("branch.feature.pushRemote", "", missing),
				gitConfigCommand("remote.pushDefault", "", missing),
				gitConfigCommand("branch.feature.remote", "", missing),
			},
			remoteName: "origin",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			commands := []expectedCommand{{
				name:      "git",
				arguments: []string{"symbolic-ref", "--quiet", "--short", "HEAD"},
				output:    "feature\n",
			}}
			commands = append(commands, test.config...)
			commands = append(commands, expectedCommand{
				name:      "git",
				arguments: []string{"remote", "get-url", "--push", test.remoteName},
				output:    "git@github.com:fork-owner/project.git\n",
			})
			runner := &scriptedRunner{t: t, commands: commands}
			git := &ExecGitContext{runner: runner}

			branch, err := git.CurrentBranch(context.Background())
			if err != nil {
				t.Fatalf("CurrentBranch() error = %v", err)
			}
			want := Branch{Name: "feature", RemoteOwner: "fork-owner"}
			if branch != want {
				t.Fatalf("CurrentBranch() = %#v, want %#v", branch, want)
			}
			runner.requireDone()
		})
	}
}

func TestExecGitContextLocalRemoteLeavesBranchUnqualified(t *testing.T) {
	runner := &scriptedRunner{
		t: t,
		commands: []expectedCommand{
			{
				name:      "git",
				arguments: []string{"symbolic-ref", "--quiet", "--short", "HEAD"},
				output:    "feature\n",
			},
			gitConfigCommand("branch.feature.pushRemote", ".\n", nil),
		},
	}

	branch, err := (&ExecGitContext{runner: runner}).CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch() error = %v", err)
	}
	if branch != (Branch{Name: "feature"}) {
		t.Fatalf("CurrentBranch() = %#v, want unqualified feature branch", branch)
	}
	runner.requireDone()
}

func TestExecGitContextReturnsUnqualifiedBranchWhenRemoteCannotBeRead(t *testing.T) {
	missing := errors.New("config value not found")
	runner := &scriptedRunner{
		t: t,
		commands: []expectedCommand{
			{
				name:      "git",
				arguments: []string{"symbolic-ref", "--quiet", "--short", "HEAD"},
				output:    "feature\n",
			},
			gitConfigCommand("branch.feature.pushRemote", "", missing),
			gitConfigCommand("remote.pushDefault", "", missing),
			gitConfigCommand("branch.feature.remote", "fork", nil),
			{
				name:      "git",
				arguments: []string{"remote", "get-url", "--push", "fork"},
				err:       errors.New("remote not found"),
			},
		},
	}

	branch, err := (&ExecGitContext{runner: runner}).CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch() error = %v", err)
	}
	if branch != (Branch{Name: "feature"}) {
		t.Fatalf("CurrentBranch() = %#v, want unqualified feature branch", branch)
	}
	runner.requireDone()
}

func TestExecGitContextRejectsDetachedHead(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		err    error
	}{
		{name: "symbolic ref fails", err: errors.New("exit status 1")},
		{name: "symbolic ref is empty", output: "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{
				t: t,
				commands: []expectedCommand{{
					name:      "git",
					arguments: []string{"symbolic-ref", "--quiet", "--short", "HEAD"},
					output:    test.output,
					err:       test.err,
				}},
			}
			_, err := (&ExecGitContext{runner: runner}).CurrentBranch(context.Background())
			failure := requireFailureCode(t, err, domain.CodeIOError)
			if failure.Details["reason"] != "detached_head" {
				t.Fatalf("Details[reason] = %#v, want detached_head", failure.Details["reason"])
			}
			runner.requireDone()
		})
	}
}

func TestExecGitContextPreservesCancellation(t *testing.T) {
	missing := errors.New("config value not found")
	for _, test := range []struct {
		name     string
		commands []expectedCommand
	}{
		{
			name: "symbolic ref",
			commands: []expectedCommand{{
				name:      "git",
				arguments: []string{"symbolic-ref", "--quiet", "--short", "HEAD"},
				err:       context.Canceled,
			}},
		},
		{
			name: "config lookup",
			commands: []expectedCommand{
				{
					name:      "git",
					arguments: []string{"symbolic-ref", "--quiet", "--short", "HEAD"},
					output:    "feature\n",
				},
				gitConfigCommand("branch.feature.pushRemote", "", context.DeadlineExceeded),
			},
		},
		{
			name: "remote lookup",
			commands: []expectedCommand{
				{
					name:      "git",
					arguments: []string{"symbolic-ref", "--quiet", "--short", "HEAD"},
					output:    "feature\n",
				},
				gitConfigCommand("branch.feature.pushRemote", "", missing),
				gitConfigCommand("remote.pushDefault", "", missing),
				gitConfigCommand("branch.feature.remote", "fork", nil),
				{
					name:      "git",
					arguments: []string{"remote", "get-url", "--push", "fork"},
					err:       context.Canceled,
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			runner := &scriptedRunner{
				t:        t,
				commands: test.commands,
				beforeReturn: func(command expectedCommand) {
					if errors.Is(command.err, context.Canceled) ||
						errors.Is(command.err, context.DeadlineExceeded) {
						cancel()
					}
				},
			}
			_, err := (&ExecGitContext{runner: runner}).CurrentBranch(ctx)
			_ = requireFailureCode(t, err, domain.CodeCancelled)
			runner.requireDone()
		})
	}
}

func TestExecGitContextDoesNotTreatLiveContextTimeoutAsCancellation(t *testing.T) {
	runner := &scriptedRunner{t: t, commands: []expectedCommand{{
		name:      "git",
		arguments: []string{"symbolic-ref", "--quiet", "--short", "HEAD"},
		err:       context.DeadlineExceeded,
	}}}
	_, err := (&ExecGitContext{runner: runner}).CurrentBranch(context.Background())
	_ = requireFailureCode(t, err, domain.CodeIOError)
	runner.requireDone()
}

func TestGitHubRemoteOwner(t *testing.T) {
	for _, test := range []struct {
		name   string
		remote string
		want   string
	}{
		{
			name:   "SSH scp syntax",
			remote: "git@github.com:octo-org/octo-repo.git",
			want:   "octo-org",
		},
		{
			name:   "SSH scp syntax without user",
			remote: "github.com:octo-org/octo-repo.git",
			want:   "octo-org",
		},
		{
			name:   "case insensitive SSH host",
			remote: "git@GITHUB.COM:Octo-Org/octo-repo.git",
			want:   "Octo-Org",
		},
		{
			name:   "HTTPS",
			remote: "https://github.com/octo-org/octo-repo.git",
			want:   "octo-org",
		},
		{
			name:   "SSH URL with port",
			remote: "ssh://git@github.com:2222/octo-org/octo-repo.git",
			want:   "octo-org",
		},
		{
			name:   "Git protocol",
			remote: "git://github.com/octo-org/octo-repo.git",
			want:   "octo-org",
		},
		{
			name:   "surrounding whitespace",
			remote: "  git@github.com:octo-org/octo-repo.git\n",
			want:   "octo-org",
		},
		{
			name:   "unsupported host",
			remote: "git@git.example.com:octo-org/octo-repo.git",
		},
		{
			name:   "lookalike host",
			remote: "https://github.com.example/octo-org/octo-repo.git",
		},
		{
			name:   "local path",
			remote: "../octo-org/octo-repo",
		},
		{
			name:   "missing repository",
			remote: "git@github.com:octo-org",
		},
		{
			name:   "extra path component",
			remote: "https://github.com/group/octo-org/octo-repo.git",
		},
		{
			name: "empty",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := githubRemoteOwner(test.remote); got != test.want {
				t.Fatalf("githubRemoteOwner(%q) = %q, want %q", test.remote, got, test.want)
			}
		})
	}
}

func gitConfigCommand(key string, output string, err error) expectedCommand {
	return expectedCommand{
		name:      "git",
		arguments: []string{"config", "--get", key},
		output:    output,
		err:       err,
	}
}
