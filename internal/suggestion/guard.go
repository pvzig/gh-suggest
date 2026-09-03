package suggestion

// BodySHA256 computes the digest used to bind an exact rendered Markdown body
// without exposing that body in output or reconciliation descriptors.
func BodySHA256(body string) string {
	return sha256Hex([]byte(body))
}
