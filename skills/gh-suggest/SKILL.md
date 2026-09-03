---
name: gh-suggest
description: Use when a code review or direct request benefits from preparing, validating, or explicitly posting one GitHub pull-request review containing one or more exact inline suggested changes through the gh-suggest CLI extension. Choose a one-item manifest for one ready finding and group multiple ready findings that belong to the same pull-request snapshot and posting decision. Review-only tasks may dry-run a review, but only a current explicit instruction to leave, post, publish, or send that exact review authorizes its external write.
---

# Post grouped GitHub suggested changes

Use `gh suggest` to prepare one submitted `COMMENT` review containing an
ordered set of inline suggestions. Require GitHub CLI 2.90.0 or newer,
authenticated `github.com` access, and matching extension/skill release pins.

## Enforce authorization

Treat review creation as one external write that triggers notifications.

- Accept only a current explicit instruction to post the exact grouped review.
- Treat review, issue-finding, and suggestion-preparation requests as
  authorization for dry-run only.
- Do not infer posting authorization from permission to edit local files.
- Inspect the review file and every replacement before a write. If the target,
  prose, order, or replacement changes after authorization, obtain fresh
  authorization before posting the changed review.

## Choose one or grouped suggestions

Use the same review-file workflow for both cases. A single suggestion is a
one-item `suggestions` array, not a separate command.

- Use one item when exactly one actionable suggestion is ready, when the user
  authorized only that finding, or when other findings need a separate review
  context or posting decision.
- Group two or more ready, non-overlapping suggestions when they target the same
  pull-request snapshot and should be reviewed or posted together. Prefer one
  group to reduce external writes and notifications.
- Split compatible findings only when they require separate timing, review
  context, or authorization, or when the count or size limits require it. Treat
  every resulting group as a separate external write with explicit posting
  authorization.
- Never silently add suggestions to an authorized review or split one
  authorized review into multiple writes. A membership, order, or partition
  change requires explicit authorization for every resulting review.
- Do not bypass overlap validation by posting alternatives separately. Choose
  one alternative or ask the user which one to prepare.
- Do not pad a group with speculative findings or delay a ready single finding
  merely to form a batch.
- Order grouped suggestions intentionally. If any member or its order changes,
  inspect the complete group again before seeking posting authorization.

## Check availability

Run:

```sh
gh --version
gh suggest --help
```

If GitHub CLI is too old, explain that skill discovery requires 2.90.0 or
newer. If the extension is missing, ask the user to install it. Do not replace
it with handwritten API calls or claim help output proves its release tag.

Once the matching tag is known, the pinned recovery command is:

```sh
gh extension install pvzig/gh-suggest --pin <TAG>
```

Never guess `<TAG>`.

## Prepare the review files

Use native file editing to create a private temporary directory. Write each
replacement exactly as it should be applied: no Markdown fence, no shell
interpolation, preserved indentation, and a zero-byte file for deletion.

Create a review file beside the replacements:

```json
{
  "schemaVersion": 1,
  "reviewBody": "Two focused correctness fixes.",
  "suggestions": [
    {
      "path": "Sources/First.swift",
      "endLine": 17,
      "replacementFile": "first.swift",
      "note": "Avoid losing cancellation."
    },
    {
      "path": "Sources/Second.swift",
      "startLine": 40,
      "endLine": 42,
      "replacementFile": "second.swift"
    }
  ]
}
```

Resolve relative `replacementFile` values from the review file's directory.
Use `startLine` only for a multi-line range; `endLine` is inclusive. Keep
suggestions ordered and non-overlapping. Close every Markdown fence opened in a
suggestion `note`.

Inspect the review file and every replacement before trusting hashes. Read
[references/cli-contract.md](references/cli-contract.md) when handling limits,
structured errors, reconciliation, or compatibility.

## Validate or post

For review-only work, dry-run the complete review:

```sh
gh suggest create <pr> \
  --repo <owner/repository> \
  --review-file <review.json> \
  --dry-run \
  --json
```

Report `posted: false` and stop.

With explicit authorization for the exact grouped review, validate and post it
once in a single invocation:

```sh
gh suggest create <pr> \
  --repo <owner/repository> \
  --review-file <review.json> \
  --json
```

If the reviewed pull-request head is already known, optionally add
`--head-sha <expected-headSHA>` so the command fails if that snapshot changed.
Do not require a preliminary dry run solely to obtain this value.

Report the returned review URL and ID. If an `io_error` contains `posted: true`,
the review exists; report it and never retry.

## Reconcile an ambiguous write

When running a write, preserve its JSON stderr in a private attempt
file. For `write_outcome_unknown`, never repeat the POST. Reconcile the saved
envelope:

```sh
gh suggest reconcile \
  --attempt-file <attempt-error.json> \
  --json
```

Report `likely_created` only when the command finds one complete matching
review. Zero, partial, or multiple matches remain `unknown` and never authorize
a retry.

Remove temporary review, replacement, and resolved attempt files after their
result is safely reported. Prefer native file deletion followed by
non-recursive empty-directory removal. If one cleanup method is denied, retry
with those safer primitives rather than leaving review content behind.
