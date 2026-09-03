package domain

import (
	"errors"
	"fmt"
	"maps"
)

// Code is a stable, machine-readable failure classification. It is the single
// definition of the vocabulary; the output package maps these values to process
// exit codes.
type Code string

// The complete failure vocabulary. Each value maps to one documented process
// exit code; see the output package for that mapping.
const (
	CodeInvalidArguments     Code = "invalid_arguments"
	CodeAuthenticationFailed Code = "authentication_failed"
	CodeTargetNotFound       Code = "target_not_found"
	CodeRangeNotCommentable  Code = "range_not_commentable"
	CodeStalePullRequest     Code = "stale_pull_request"
	CodeGitHubAPIError       Code = "github_api_error"
	CodeIOError              Code = "io_error"
	CodeRateLimited          Code = "rate_limited"
	CodeWriteOutcomeUnknown  Code = "write_outcome_unknown"
	CodeUnsupportedHost      Code = "unsupported_host"
	CodeCancelled            Code = "cancelled"
	CodePermissionDenied     Code = "permission_denied"
)

// Known reports whether code is part of the documented vocabulary. Callers that
// serialize a code use this to avoid emitting a classification the contract does
// not define.
func (code Code) Known() bool {
	switch code {
	case CodeInvalidArguments,
		CodeAuthenticationFailed,
		CodeTargetNotFound,
		CodeRangeNotCommentable,
		CodeStalePullRequest,
		CodeGitHubAPIError,
		CodeIOError,
		CodeRateLimited,
		CodeWriteOutcomeUnknown,
		CodeUnsupportedHost,
		CodeCancelled,
		CodePermissionDenied:
		return true
	default:
		return false
	}
}

// Failure is a stable application failure. Cause is retained for tests and
// diagnostics but is never serialized by the output package.
type Failure struct {
	Code    Code
	Message string
	Details map[string]any
	Cause   error
}

func (failure *Failure) Error() string {
	if failure.Cause == nil {
		return failure.Message
	}
	return fmt.Sprintf("%s: %v", failure.Message, failure.Cause)
}

func (failure *Failure) Unwrap() error {
	return failure.Cause
}

// NewFailure constructs a failure and shallow-copies its details map.
func NewFailure(code Code, message string, details map[string]any, cause error) *Failure {
	return &Failure{
		Code:    code,
		Message: message,
		Details: CloneDetails(details),
		Cause:   cause,
	}
}

// AsFailure reports whether err is, or wraps, a stable Failure.
func AsFailure(err error) (*Failure, bool) {
	return errors.AsType[*Failure](err)
}

// CloneDetails shallow-copies a details map. Reference-valued entries remain
// shared; callers must treat them as immutable. A nil map stays nil so it is
// omitted from output.
func CloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	cloned := make(map[string]any, len(details))
	maps.Copy(cloned, details)
	return cloned
}
