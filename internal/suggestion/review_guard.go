package suggestion

import "encoding/json"

const (
	// ReviewRequestDigestVersion identifies the canonical grouped-review digest
	// schema.
	ReviewRequestDigestVersion = 1
)

// ReviewCommentDigestInput contains one ordered inline comment bound by a
// grouped-review request digest.
type ReviewCommentDigestInput struct {
	Path      string
	StartLine *int
	EndLine   int
	Side      string
	Body      string
}

// ReviewRequestDigestInput contains every caller-controlled value bound by a
// grouped-review request digest. ReviewBody is the exact visible, normalized
// review summary sent to GitHub.
type ReviewRequestDigestInput struct {
	Host        string
	Owner       string
	Repository  string
	PullRequest int
	BaseSHA     string
	HeadSHA     string
	Event       string
	ReviewBody  string
	Comments    []ReviewCommentDigestInput
}

// ComputeReviewRequestSHA256 computes a digest over a fixed-field, versioned
// canonical JSON representation. Comment order is significant because it is
// also the order sent to GitHub.
func ComputeReviewRequestSHA256(input ReviewRequestDigestInput) (string, error) {
	encoded, err := marshalReviewRequestDigest(input)
	if err != nil {
		return "", err
	}

	return sha256Hex(encoded), nil
}

func marshalReviewRequestDigest(input ReviewRequestDigestInput) ([]byte, error) {
	type canonicalComment struct {
		Path       string `json:"path"`
		StartLine  *int   `json:"startLine,omitempty"`
		EndLine    int    `json:"endLine"`
		Side       string `json:"side"`
		BodySHA256 string `json:"bodySHA256"`
	}

	comments := make([]canonicalComment, len(input.Comments))
	for index, comment := range input.Comments {
		comments[index] = canonicalComment{
			Path:       comment.Path,
			StartLine:  comment.StartLine,
			EndLine:    comment.EndLine,
			Side:       comment.Side,
			BodySHA256: BodySHA256(comment.Body),
		}
	}

	// Field order is part of the version 1 grouped-review digest contract.
	canonical := struct {
		DigestVersion    int                `json:"digestVersion"`
		Host             string             `json:"host"`
		Owner            string             `json:"owner"`
		Repository       string             `json:"repository"`
		PullRequest      int                `json:"pullRequest"`
		BaseSHA          string             `json:"baseSHA"`
		HeadSHA          string             `json:"headSHA"`
		Event            string             `json:"event"`
		ReviewBodySHA256 string             `json:"reviewBodySHA256"`
		Comments         []canonicalComment `json:"comments"`
	}{
		DigestVersion:    ReviewRequestDigestVersion,
		Host:             input.Host,
		Owner:            input.Owner,
		Repository:       input.Repository,
		PullRequest:      input.PullRequest,
		BaseSHA:          input.BaseSHA,
		HeadSHA:          input.HeadSHA,
		Event:            input.Event,
		ReviewBodySHA256: BodySHA256(input.ReviewBody),
		Comments:         comments,
	}

	return json.Marshal(canonical)
}
