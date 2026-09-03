// Package cli owns process argument parsing, bounded local file input, and
// stable process output.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/pvzig/gh-suggest/internal/create"
	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/output"
	"github.com/pvzig/gh-suggest/internal/reconcile"
	"github.com/pvzig/gh-suggest/internal/suggestion"
)

const usage = `gh suggest creates one validated pull-request review containing one or more inline suggestions.

Usage:
  gh suggest create [<number> | <url> | <branch>] [flags]
  gh suggest reconcile --attempt-file PATH [flags]

Run "gh suggest <command> --help" for command flags.
`

const createUsage = `Usage:
  gh suggest create [<number> | <url> | <branch>] --review-file PATH [flags]

Flags:
  -R, --repo [HOST/]OWNER/REPO
  --review-file PATH
  --head-sha SHA
  --dry-run
  --json
`

const reconcileUsage = `Usage:
  gh suggest reconcile --attempt-file PATH [flags]

Flags:
  --attempt-file PATH
  --json
`

const maxRawReplacementBytes = 2 * (suggestion.MaxReplacementBytes + 1)

var errReplacementTooLarge = errors.New("replacement input exceeds the size limit")

// Creator executes the grouped-review create use case.
type Creator interface {
	Preflight(ctx context.Context, request create.Request) error
	Execute(ctx context.Context, request create.Request) (create.Result, error)
}

// Reconciler executes whole-review, read-only reconciliation.
type Reconciler interface {
	Execute(ctx context.Context, request reconcile.Request) (reconcile.Result, error)
}

// Runner parses arguments and dispatches to application services.
type Runner struct {
	creator    Creator
	reconciler Reconciler
}

// New builds a runner over create and reconciliation.
func New(creator Creator, reconciler Reconciler) *Runner {
	return &Runner{creator: creator, reconciler: reconciler}
}

// Run emits only to the provided streams and returns the process exit code.
func (runner *Runner) Run(
	ctx context.Context,
	arguments []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if len(arguments) == 0 {
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	switch arguments[0] {
	case "help", "-h", "--help":
		_, _ = io.WriteString(stdout, usage)
		return 0
	case "create":
		return runner.runCreate(ctx, arguments[1:], stdin, stdout, stderr)
	case "reconcile":
		return runner.runReconcile(ctx, arguments[1:], stdin, stdout, stderr)
	default:
		return writeFailure(
			stderr,
			false,
			domain.NewFailure(
				domain.CodeInvalidArguments,
				"Unknown command. Expected create or reconcile.",
				map[string]any{"command": arguments[0]},
				nil,
			),
		)
	}
}

func (runner *Runner) runCreate(
	ctx context.Context,
	arguments []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) int {
	selector, flagArguments := extractSelector(arguments)
	jsonFallback := containsExactJSONFlag(flagArguments)

	var (
		repository     string
		reviewFilePath string
		headSHA        string
		dryRun         bool
		jsonMode       bool
	)
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "")
	flags.StringVar(&repository, "R", "", "")
	flags.StringVar(&reviewFilePath, "review-file", "", "")
	flags.StringVar(&headSHA, "head-sha", "", "")
	flags.BoolVar(&dryRun, "dry-run", false, "")
	flags.BoolVar(&jsonMode, "json", false, "")

	if err := flags.Parse(flagArguments); err != nil {
		if errors.Is(err, flag.ErrHelp) && !jsonFallback {
			_, _ = io.WriteString(stdout, createUsage)
			return 0
		}
		return parseFailure(stderr, jsonFallback, err)
	}
	jsonMode = jsonMode || jsonFallback
	if flags.NArg() != 0 {
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeInvalidArguments,
				"Only one leading pull-request selector is allowed.",
				map[string]any{"unexpected": flags.Args()},
				nil,
			),
		)
	}
	if reviewFilePath == "" {
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeInvalidArguments,
				"--review-file is required.",
				nil,
				nil,
			),
		)
	}

	manifest, baseDirectory, failure := parseReviewFile(reviewFilePath, stdin)
	if failure != nil {
		return writeFailure(stderr, jsonMode, failure)
	}
	request := requestFromReviewFile(
		manifest,
		selector,
		repository,
		dryRun,
		headSHA,
	)
	if err := runner.creator.Preflight(ctx, request); err != nil {
		if failure, stable := domain.AsFailure(err); stable {
			return writeFailure(stderr, jsonMode, failure)
		}
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeGitHubAPIError,
				"The review preflight failed.",
				nil,
				err,
			),
		)
	}
	if failure := readReviewReplacements(
		&request,
		manifest,
		baseDirectory,
		reviewFilePath,
		stdin,
	); failure != nil {
		return writeFailure(stderr, jsonMode, failure)
	}

	result, err := runner.creator.Execute(ctx, request)
	if err != nil {
		if failure, stable := domain.AsFailure(err); stable {
			return writeFailure(stderr, jsonMode, failure)
		}
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeGitHubAPIError,
				"The review operation failed.",
				nil,
				err,
			),
		)
	}
	success, err := makeSuccess(result)
	if err != nil {
		return writeFailure(
			stderr,
			jsonMode,
			resultOutputFailure(
				result,
				domain.CodeGitHubAPIError,
				"The successful result could not be encoded.",
				err,
			),
		)
	}
	if err := output.WriteSuccess(stdout, success, jsonMode); err != nil {
		message := "The validated review result could not be written."
		return writeFailure(
			stderr,
			jsonMode,
			resultOutputFailure(result, domain.CodeIOError, message, err),
		)
	}
	return 0
}

func resultOutputFailure(
	result create.Result,
	code domain.Code,
	message string,
	cause error,
) *domain.Failure {
	details := map[string]any{
		"posted":        result.Posted,
		"requestSHA256": result.RequestSHA256,
	}
	if result.Posted {
		code = domain.CodeIOError
		message = "The review was created, but its success result could not be finalized."
		details["reviewID"] = result.ReviewID
		details["url"] = result.URL
		details["guidance"] = "The review was created; do not retry the write."
	}
	return domain.NewFailure(code, message, details, cause)
}

func (runner *Runner) runReconcile(
	ctx context.Context,
	arguments []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) int {
	jsonFallback := containsExactJSONFlag(arguments)
	var (
		attemptFilePath string
		jsonMode        bool
	)
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&attemptFilePath, "attempt-file", "", "")
	flags.BoolVar(&jsonMode, "json", false, "")

	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) && !jsonFallback {
			_, _ = io.WriteString(stdout, reconcileUsage)
			return 0
		}
		return parseFailure(stderr, jsonFallback, err)
	}
	if flags.NArg() != 0 {
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeInvalidArguments,
				"The reconcile command accepts only flags.",
				map[string]any{"unexpected": flags.Args()},
				nil,
			),
		)
	}
	if attemptFilePath == "" {
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeInvalidArguments,
				"--attempt-file is required.",
				nil,
				nil,
			),
		)
	}
	if runner.reconciler == nil {
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeGitHubAPIError,
				"The reconciliation service is unavailable.",
				nil,
				nil,
			),
		)
	}
	request, failure := parseAttemptFile(attemptFilePath, stdin)
	if failure != nil {
		return writeFailure(stderr, jsonMode, failure)
	}
	result, err := runner.reconciler.Execute(ctx, request)
	if err != nil {
		if failure, stable := domain.AsFailure(err); stable {
			return writeFailure(stderr, jsonMode, failure)
		}
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeGitHubAPIError,
				"The reconciliation operation failed.",
				nil,
				err,
			),
		)
	}

	reconciliation := makeReconciliation(result)
	if err := output.WriteReconciliation(stdout, reconciliation, jsonMode); err != nil {
		return writeFailure(
			stderr,
			jsonMode,
			domain.NewFailure(
				domain.CodeIOError,
				"The reconciliation result could not be written.",
				nil,
				err,
			),
		)
	}
	return 0
}

func parseFailure(writer io.Writer, jsonMode bool, err error) int {
	return writeFailure(
		writer,
		jsonMode,
		domain.NewFailure(
			domain.CodeInvalidArguments,
			"Command arguments are invalid.",
			map[string]any{"reason": conciseParseError(err)},
			err,
		),
	)
}

func extractSelector(arguments []string) (string, []string) {
	if len(arguments) == 0 || strings.HasPrefix(arguments[0], "-") {
		return "", arguments
	}
	return arguments[0], arguments[1:]
}

func containsExactJSONFlag(arguments []string) bool {
	return slices.Contains(arguments, "--json")
}

func conciseParseError(err error) string {
	message := err.Error()
	if firstLine, _, found := strings.Cut(message, "\n"); found {
		return firstLine
	}
	return message
}

func readReplacement(path string, stdin io.Reader) ([]byte, error) {
	content, err := readBoundedInput(path, stdin, maxRawReplacementBytes)
	if err != nil {
		if errors.Is(err, errInputTooLarge) {
			return nil, fmt.Errorf(
				"%w: maximum is %d normalized bytes",
				errReplacementTooLarge,
				suggestion.MaxReplacementBytes,
			)
		}
		return nil, err
	}
	if suggestion.ExceedsReplacementLimit(content) {
		return nil, fmt.Errorf(
			"%w: maximum is %d normalized bytes",
			errReplacementTooLarge,
			suggestion.MaxReplacementBytes,
		)
	}
	return content, nil
}

func makeSuccess(result create.Result) (output.Success, error) {
	suggestions := make([]output.Suggestion, len(result.Suggestions))
	for index, suggestionResult := range result.Suggestions {
		startLine := suggestionResult.EndLine
		if suggestionResult.StartLine != nil {
			startLine = *suggestionResult.StartLine
		}
		suggestions[index] = output.Suggestion{
			Path: suggestionResult.Path,
			Range: output.Range{
				StartLine: startLine,
				EndLine:   suggestionResult.EndLine,
				Side:      domain.SideRight,
			},
			Replacement: output.Replacement{
				ByteCount: suggestionResult.Replacement.ByteCount,
				LineCount: suggestionResult.Replacement.LineCount,
				SHA256:    suggestionResult.Replacement.SHA256,
			},
			BodySHA256: suggestionResult.BodySHA256,
		}
	}
	target := output.Target{
		Host:             result.Host,
		Repository:       result.Repository,
		PullRequest:      result.PullRequest,
		PullRequestURL:   result.PullRequestURL,
		BaseSHA:          result.BaseSHA,
		HeadSHA:          result.HeadSHA,
		ReviewBodySHA256: result.ReviewBodySHA256,
		Suggestions:      suggestions,
		RequestSHA256:    result.RequestSHA256,
	}
	if result.Posted {
		return output.NewCreated(target, result.ReviewID, result.URL)
	}
	return output.NewValidated(target)
}

func makeReconciliation(result reconcile.Result) output.Reconciliation {
	reconciliation := output.Reconciliation{
		SchemaVersion:     output.SchemaVersion,
		Status:            result.Status,
		Host:              result.Host,
		Repository:        result.Repository,
		PullRequest:       result.PullRequest,
		HeadSHA:           result.HeadSHA,
		RequestSHA256:     result.RequestSHA256,
		AttemptStartedAt:  result.AttemptStartedAt.UTC().Format(time.RFC3339Nano),
		SuggestionCount:   result.SuggestionCount,
		CandidateCount:    result.CandidateCount,
		PartialMatchCount: result.PartialMatchCount,
		MatchCount:        result.MatchCount,
		URL:               result.URL,
	}
	if result.Status == domain.ReconciliationLikelyCreated {
		reviewID := result.ReviewID
		reconciliation.ReviewID = &reviewID
	}
	return reconciliation
}

func writeFailure(writer io.Writer, jsonMode bool, failure *domain.Failure) int {
	stableFailure := output.NewFailure(
		failure.Code,
		failure.Message,
		failure.Details,
	)
	// Reconciliation consumes the complete error envelope. Preserve that
	// machine-readable recovery artifact even when the caller selected human
	// output for the review write.
	jsonMode = jsonMode || stableFailure.Code == domain.CodeWriteOutcomeUnknown
	_ = output.WriteFailure(writer, stableFailure, jsonMode)
	return stableFailure.ExitCode()
}
