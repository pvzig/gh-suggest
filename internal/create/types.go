// Package create implements the prepare-and-post use case for grouped
// pull-request suggestions. Transport, Git, and process concerns are supplied
// through narrow interfaces at the application boundary.
package create

import (
	"context"

	"github.com/pvzig/gh-suggest/internal/domain"
)

// MaxSuggestions bounds one grouped review before any external reads occur.
const MaxSuggestions = domain.MaxSuggestions

// Suggestion contains one inline replacement already read from its
// caller-selected file. Slice order is preserved through validation, hashing,
// transport, and dry-run output.
type Suggestion struct {
	Path        string
	StartLine   *int
	EndLine     int
	Replacement []byte
	Note        string
}

// ReviewComment is one exact rendered inline comment sent as part of a grouped
// GitHub review.
type ReviewComment struct {
	Body      string
	Path      string
	StartLine *int
	EndLine   int
}

// ReviewRequest contains the exact grouped-review payload and its validated
// snapshot. BaseSHA and RequestSHA256 are not sent as GitHub JSON fields;
// they travel with the port value so an ambiguous adapter result can emit a
// complete, non-sensitive reconciliation descriptor.
type ReviewRequest struct {
	Ref           domain.PullRequestRef
	BaseSHA       string
	HeadSHA       string
	Event         string
	Body          string
	Comments      []ReviewComment
	RequestSHA256 string
}

// CreatedReview is the definitive response required to report a successful
// external write.
type CreatedReview struct {
	ID  int64
	URL string
}

// Request is independent of process streams and local file access. ReviewBody
// and each suggestion's replacement bytes have already been read by the caller.
type Request struct {
	Selector        string
	Repository      string
	ReviewBody      string
	Suggestions     []Suggestion
	DryRun          bool
	ExpectedHeadSHA string
}

// ReplacementSummary describes exact normalized replacement text without
// exposing it.
type ReplacementSummary struct {
	ByteCount int
	LineCount int
	SHA256    string
}

// SuggestionSummary describes one validated inline suggestion without exposing
// its note, replacement, or rendered Markdown body.
type SuggestionSummary struct {
	Path        string
	StartLine   *int
	EndLine     int
	Replacement ReplacementSummary
	BodySHA256  string
}

// Result represents both validated dry runs and definitively created reviews.
// Posted is false for a dry run and true only after GitHub returned a
// definitive created-review response.
type Result struct {
	Host             string
	Repository       string
	PullRequest      int
	PullRequestURL   string
	BaseSHA          string
	HeadSHA          string
	ReviewBodySHA256 string
	Suggestions      []SuggestionSummary
	RequestSHA256    string
	Posted           bool
	ReviewID         int64
	URL              string
}

// PullRequestResolver owns selector semantics while remaining independent of
// the create orchestration.
type PullRequestResolver interface {
	Resolve(ctx context.Context, selector string, repository string) (domain.PullRequestRef, error)
}

// PullRequestReader reads the mutable metadata used for stale-state checks.
type PullRequestReader interface {
	ReadPullRequest(ctx context.Context, ref domain.PullRequestRef) (domain.PullRequest, error)
}

// DiffReader retrieves the complete raw pull-request diff.
type DiffReader interface {
	ReadDiff(ctx context.Context, ref domain.PullRequestRef) ([]byte, error)
}

// ReviewCreator performs the single external write.
type ReviewCreator interface {
	CreateReview(ctx context.Context, request ReviewRequest) (CreatedReview, error)
}
