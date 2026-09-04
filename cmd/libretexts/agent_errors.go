package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
)

type agentError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
	Retryable bool   `json:"retryable"`
	cause     error
}

type errorEnvelope struct {
	SchemaVersion string     `json:"schema_version"`
	OK            bool       `json:"ok"`
	Error         agentError `json:"error"`
}

type globalOptions struct {
	JSONErrors bool
}

func newAgentError(code, message, hint string, retryable bool, cause error) *agentError {
	return &agentError{Code: code, Message: message, Hint: hint, Retryable: retryable, cause: cause}
}

func upstreamHTTPError(message string, statusCode int) *agentError {
	retryable := statusCode == 429 || statusCode >= 500
	return newAgentError("UPSTREAM_HTTP_ERROR", message, "Check the LibreTexts service response and retry the request.", retryable, nil)
}

func (e *agentError) Error() string {
	return e.Message
}

func (e *agentError) Unwrap() error {
	return e.cause
}

func classifyError(err error) *agentError {
	var coded *agentError
	if errors.As(err, &coded) {
		return coded
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return newAgentError("NETWORK_ERROR", err.Error(), "Check your network connection and retry the request.", true, err)
	}

	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return newAgentError("NETWORK_ERROR", err.Error(), "Check your network connection and retry the request.", true, err)
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return newAgentError("FILESYSTEM_ERROR", err.Error(), "Check the target path and its permissions, then retry.", false, err)
	}

	return newAgentError("INTERNAL_ERROR", err.Error(), "Retry the command and report the error if it persists.", false, err)
}

func extractGlobalFlags(args []string) ([]string, globalOptions) {
	filtered := make([]string, 0, len(args))
	var options globalOptions
	for _, arg := range args {
		if arg == "--json-errors" {
			options.JSONErrors = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, options
}

// newCommandFlagSet always continues on error. flag.ExitOnError would call
// os.Exit(2) from inside the flag package, skipping the error envelope and
// returning a different exit code than every other failure.
func newCommandFlagSet(name string, jsonErrors bool) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func invalidArgumentError(command string, err error) *agentError {
	hint := "Check the command arguments and retry."
	if command != "" {
		hint = fmt.Sprintf("Run libretexts describe --json to list the valid options for %s.", command)
	}
	return newAgentError("INVALID_ARGUMENT", err.Error(), hint, false, err)
}

func writeCLIError(w io.Writer, err error, jsonErrors bool) error {
	if !jsonErrors {
		_, writeErr := fmt.Fprintf(w, "error: %v\n", err)
		return writeErr
	}
	return json.NewEncoder(w).Encode(errorEnvelope{
		SchemaVersion: "1",
		OK:            false,
		Error:         *classifyError(err),
	})
}

func execute(ctx context.Context, args []string, stderr io.Writer) int {
	args, options := extractGlobalFlags(args)
	if err := runWithOptions(ctx, args, options); err != nil {
		_ = writeCLIError(stderr, err, options.JSONErrors)
		return 1
	}
	return 0
}
