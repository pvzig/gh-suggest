package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var errInputTooLarge = errors.New("input exceeds the size limit")

func readBoundedInput(
	path string,
	stdin io.Reader,
	maximumBytes int,
) ([]byte, error) {
	reader := stdin
	var file *os.File
	if !isStandardInputPath(path) {
		opened, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		file = opened
		reader = file
	}

	content, err := io.ReadAll(io.LimitReader(reader, int64(maximumBytes)+1))
	if file != nil {
		closeError := file.Close()
		if err == nil {
			err = closeError
		}
	}
	if err != nil {
		return nil, err
	}
	if len(content) > maximumBytes {
		return nil, fmt.Errorf("%w: maximum is %d bytes", errInputTooLarge, maximumBytes)
	}
	return content, nil
}

// isStandardInputPath recognizes the portable sentinel and common operating-
// system aliases for file descriptor zero. Treating aliases as ordinary files
// can reopen an already-drained stdin and turn an accidental zero-byte read
// into an intentional deletion suggestion.
func isStandardInputPath(path string) bool {
	if path == "-" {
		return true
	}
	switch filepath.Clean(path) {
	case "/dev/stdin", "/dev/fd/0", "/proc/self/fd/0":
		return true
	default:
		return false
	}
}
