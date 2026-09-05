//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pvzig/gh-suggest/internal/domain"
	"github.com/pvzig/gh-suggest/internal/output"
)

func TestRunPreservesCreatedReviewAfterBrokenPipe(t *testing.T) {
	// Run the real entry point in a child: an in-process fake writer cannot
	// reproduce Go's SIGPIPE behavior on file descriptor 1.
	if reviewFile := os.Getenv("GH_SUGGEST_TEST_REVIEW_FILE"); reviewFile != "" {
		os.Args = []string{
			"gh-suggest", "create", "42", "--repo", "octo/example",
			"--review-file", reviewFile, "--json",
		}
		os.Exit(run())
	}

	// Keep the socket path below Unix-domain path limits on macOS too.
	directory, err := os.MkdirTemp("/tmp", "ghs-pipe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socketPath := filepath.Join(directory, "api.sock")
	reviewFile := filepath.Join(directory, "review.json")
	for name, content := range map[string]string{
		"config.yml": "http_unix_socket: " + socketPath + "\n",
		"review.json": `{"schemaVersion":1,"reviewBody":"Focused fix.",` +
			`"suggestions":[{"path":"example.go","endLine":1,"replacementFile":"replacement.txt"}]}`,
		"replacement.txt": "replacement\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	var postCount atomic.Int32
	const reviewURL = "https://github.com/octo/example/pull/42#pullrequestreview-99"
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/repos/octo/example/pulls/42/reviews":
			postCount.Add(1)
			if _, err := io.Copy(io.Discard, request.Body); err != nil {
				t.Error(err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(writer, `{"id":99,"html_url":"`+reviewURL+`"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/repos/octo/example/pulls/42":
			if request.Header.Get("Accept") == "application/vnd.github.diff" {
				writer.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(writer, "diff --git a/example.go b/example.go\n"+
					"--- a/example.go\n+++ b/example.go\n@@ -1 +1 @@\n-old\n+new\n")
				return
			}
			_, _ = io.WriteString(writer, `{"number":42,"state":"open","changed_files":1,`+
				`"html_url":"https://github.com/octo/example/pull/42",`+
				`"base":{"sha":"`+strings.Repeat("a", 40)+`"},`+
				`"head":{"sha":"`+strings.Repeat("b", 40)+`"}}`)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.NotFound(writer, request)
		}
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunPreservesCreatedReviewAfterBrokenPipe$")
	command.Env = append(os.Environ(),
		"GH_CONFIG_DIR="+directory,
		"GH_TOKEN=test-token",
		"GH_SUGGEST_TEST_REVIEW_FILE="+reviewFile,
	)
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err = command.Run()
	exitError, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exitError.ExitCode() != output.ExitCode(domain.CodeIOError) {
		t.Fatalf("child error = %v, want exit 8; stderr = %s", err, stderr.String())
	}
	if postCount.Load() != 1 {
		t.Fatalf("POST count = %d, want 1", postCount.Load())
	}
	var envelope output.ErrorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("decode recovery output: %v; stderr = %s", err, stderr.String())
	}
	details := envelope.Error.Details
	if envelope.Error.Code != domain.CodeIOError ||
		details["posted"] != true || details["reviewID"] != float64(99) || details["url"] != reviewURL ||
		details["guidance"] != "The review was created; do not retry the write." {
		t.Fatalf("recovery output = %#v", envelope)
	}
}
