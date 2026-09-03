package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBoundedInputSupportsFilesAndStandardInput(t *testing.T) {
	t.Parallel()

	content, err := readBoundedInput("-", strings.NewReader("stdin"), len("stdin"))
	if err != nil || string(content) != "stdin" {
		t.Fatalf("stdin content/error = %q/%v", content, err)
	}

	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err = readBoundedInput(path, strings.NewReader("unused"), len("file"))
	if err != nil || string(content) != "file" {
		t.Fatalf("file content/error = %q/%v", content, err)
	}

	if _, err := readBoundedInput("-", strings.NewReader("large"), 4); !errors.Is(err, errInputTooLarge) {
		t.Fatalf("oversized error = %v", err)
	}
}
