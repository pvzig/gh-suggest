package domain

import (
	"encoding/hex"
	"path"
	"strings"
)

// IsSupportedHost reports whether value names the version 1 GitHub host.
func IsSupportedHost(value string) bool {
	return strings.EqualFold(value, SupportedHost)
}

// ValidHexDigest reports whether value is a non-empty, even-length run of
// hexadecimal digits. Callers check the expected length separately, because a
// well-formed digest of the wrong width is a different failure than malformed
// input.
func ValidHexDigest(value string) bool {
	if len(value) == 0 || len(value)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ValidCommitSHA reports whether value is a full 40-character hexadecimal
// commit SHA.
func ValidCommitSHA(value string) bool {
	return len(value) == CommitSHALength && ValidHexDigest(value)
}

// ValidSHA256Hex reports whether value is a 64-character hexadecimal SHA-256
// digest.
func ValidSHA256Hex(value string) bool {
	return len(value) == SHA256HexLength && ValidHexDigest(value)
}

// ValidRepositoryOwner reports whether value follows GitHub's repository-owner
// rules, including underscores used by Enterprise Managed User names.
func ValidRepositoryOwner(value string) bool {
	if len(value) == 0 ||
		len(value) > 39 ||
		value[0] == '-' ||
		value[len(value)-1] == '-' {
		return false
	}

	previousHyphen := false
	for index := range len(value) {
		character := value[index]
		isAlphanumeric := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if isAlphanumeric {
			previousHyphen = false
			continue
		}
		if character == '_' {
			previousHyphen = false
			continue
		}
		if character != '-' || previousHyphen {
			return false
		}
		previousHyphen = true
	}
	return true
}

// ValidRepositoryName reports whether value is a non-special GitHub repository
// name of at most 100 alphanumeric, period, hyphen, or underscore characters.
func ValidRepositoryName(value string) bool {
	if len(value) == 0 || len(value) > 100 || value == "." || value == ".." {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' ||
			character == '-' ||
			character == '_' {
			continue
		}
		return false
	}
	return true
}

// ValidRepositoryPath reports whether value addresses a file inside the
// repository: non-empty, relative, already in cleaned form, and not escaping the
// repository root.
func ValidRepositoryPath(value string) bool {
	return value != "" &&
		!strings.ContainsRune(value, '\x00') &&
		!path.IsAbs(value) &&
		path.Clean(value) == value &&
		value != "." &&
		value != ".." &&
		!strings.HasPrefix(value, "../")
}
