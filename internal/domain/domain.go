// Package domain contains the vocabulary shared by every gh-suggest use case:
// the addresses of GitHub targets, the stable failure classification, and the
// validation rules that more than one command depends on.
//
// It deliberately depends on nothing else in the module, so transport, process,
// and Git concerns can all describe the same target without depending on one
// another.
package domain

import (
	"context"
	"crypto/sha256"
	"errors"
)

const (
	// CommitSHALength is the length of a full hexadecimal Git commit SHA. An
	// abbreviated SHA cannot be compared with the SHAs GitHub reports, so it is
	// rejected as an argument rather than misreported as a stale pull request or
	// an unmatched comment.
	CommitSHALength = 40

	// SHA256HexLength is the length of a hex-encoded SHA-256 digest.
	SHA256HexLength = sha256.Size * 2

	// MaxSuggestions is the maximum number of inline suggestions in one grouped
	// review or reconciliation descriptor.
	MaxSuggestions = 100

	// SupportedHost is the only GitHub host accepted by version 1.
	SupportedHost = "github.com"
)

const (
	// StateOpen is the only pull-request state that can accept a new suggestion.
	StateOpen = "open"

	// ReviewStateCommented is the submitted review state created and reconciled by
	// this extension.
	ReviewStateCommented = "COMMENTED"

	// ReviewEventComment is the only review action this extension may create.
	ReviewEventComment = "COMMENT"
)

// Side identifies one side of a pull-request diff.
type Side string

// SideRight is the post-image side of a diff. Version 1 addresses only it.
const SideRight Side = "RIGHT"

// ContextCancellationFailure returns a stable cancellation only when the
// caller's context was actually cancelled or reached its deadline. Transport
// timeouts may wrap context.DeadlineExceeded while the caller context remains
// live, so classifying from the operation error alone is not sufficient.
func ContextCancellationFailure(
	ctx context.Context,
	cause error,
	message string,
) *Failure {
	contextError := ctx.Err()
	if contextError == nil {
		return nil
	}
	if cause == nil {
		cause = contextError
	} else if !errors.Is(cause, contextError) {
		cause = errors.Join(cause, contextError)
	}
	return NewFailure(CodeCancelled, message, nil, cause)
}

// IsCancellation reports whether err is a stable cancellation or the supplied
// caller context has ended.
func IsCancellation(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return true
	}
	failure, ok := AsFailure(err)
	return ok && failure.Code == CodeCancelled
}

// NormalizeFailure preserves a stable failure, reports actual caller-context
// cancellation, or wraps an otherwise unclassified error with the supplied
// fallback contract.
func NormalizeFailure(
	ctx context.Context,
	err error,
	fallbackCode Code,
	fallbackMessage string,
	cancellationMessage string,
) *Failure {
	if failure, ok := AsFailure(err); ok {
		return failure
	}
	if failure := ContextCancellationFailure(ctx, err, cancellationMessage); failure != nil {
		return failure
	}
	return NewFailure(fallbackCode, fallbackMessage, nil, err)
}

// ReconciliationStatus reports what read-only reconciliation could prove about
// an ambiguous write.
type ReconciliationStatus string

const (
	// ReconciliationLikelyCreated means exactly one submitted review and its
	// complete inline-comment set match the recorded attempt.
	ReconciliationLikelyCreated ReconciliationStatus = "likely_created"
	// ReconciliationUnknown means zero or several complete reviews match, so
	// the write remains unresolved.
	ReconciliationUnknown ReconciliationStatus = "unknown"
)

// Repository identifies one repository on a GitHub host.
type Repository struct {
	Host  string
	Owner string
	Name  string
}

// String returns the OWNER/REPOSITORY form, without the host.
func (repository Repository) String() string {
	return repository.Owner + "/" + repository.Name
}

// PullRequestRef is the stable address of a pull request.
type PullRequestRef struct {
	Repository Repository
	Number     int
}

// PullRequest contains the mutable metadata observed at one point in time.
type PullRequest struct {
	Ref          PullRequestRef
	URL          string
	State        string
	BaseSHA      string
	HeadSHA      string
	ChangedFiles int
}
