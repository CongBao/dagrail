package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/CongBao/dagrail/internal/commandcatalog"
)

const ErrorAPIVersion = "dagrail.io/cli-error/v1alpha1"

type codedError struct {
	code      string
	category  string
	exitCode  int
	retryable bool
	err       error
}

func (err *codedError) Error() string { return err.err.Error() }
func (err *codedError) Unwrap() error { return err.err }

type ErrorReport struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Code       string `json:"code"`
	Category   string `json:"category"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	ExitCode   int    `json:"exitCode"`
}

func usagef(format string, args ...any) error {
	return &codedError{code: "usage", category: "usage", exitCode: commandcatalog.ExitUsage, err: fmt.Errorf(format, args...)}
}

func diagnosticError(err error) error {
	if err == nil {
		err = errors.New("diagnostic checks did not pass")
	}
	return &codedError{code: "diagnostic_failed", category: "diagnostic", exitCode: commandcatalog.ExitDiagnostic, err: err}
}

func normalizeDispatchError(err error) error {
	if err == nil {
		return nil
	}
	var typed *codedError
	if errors.As(err, &typed) || errors.Is(err, context.Canceled) {
		return err
	}
	message := err.Error()
	if errors.Is(err, flag.ErrHelp) || strings.HasPrefix(message, "usage: dagrail") || strings.HasPrefix(message, "flag provided but not defined:") || strings.HasPrefix(message, "invalid value ") || strings.HasPrefix(message, "expected argument for flag") || (strings.HasPrefix(message, "unknown ") && strings.Contains(message, " command ")) {
		return &codedError{code: "usage", category: "usage", exitCode: commandcatalog.ExitUsage, err: err}
	}
	return err
}

func DescribeError(err error) ErrorReport {
	report := ErrorReport{APIVersion: ErrorAPIVersion, Kind: "CLIError", Code: "operation_failed", Category: "operation", Message: boundedErrorMessage(err), ExitCode: commandcatalog.ExitOperationFailed}
	var typed *codedError
	if errors.As(err, &typed) {
		report.Code, report.Category, report.Retryable, report.ExitCode = typed.code, typed.category, typed.retryable, typed.exitCode
	} else if errors.Is(err, context.Canceled) {
		report.Code, report.Category, report.Message = "interrupted", "interruption", "operation interrupted"
		report.Retryable, report.ExitCode = true, commandcatalog.ExitInterrupted
	} else if errors.Is(err, flag.ErrHelp) {
		report.Code, report.Category, report.ExitCode = "usage", "usage", commandcatalog.ExitUsage
	}
	return report
}

func WriteErrorJSON(writer io.Writer, err error) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(DescribeError(err))
}

func WantsJSONErrors(args []string) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DAGRAIL_ERROR_FORMAT")), "json") {
		return true
	}
	for index, arg := range args {
		if arg == "--errors=json" || (arg == "--errors" && index+1 < len(args) && args[index+1] == "json") {
			return true
		}
	}
	return false
}

func consumeGlobalArgs(args []string) ([]string, error) {
	result := append([]string{}, args...)
	for len(result) > 0 {
		switch {
		case result[0] == "--errors=json":
			result = result[1:]
		case result[0] == "--errors":
			if len(result) < 2 || result[1] != "json" {
				return nil, usagef("--errors supports only json")
			}
			result = result[2:]
		case strings.HasPrefix(result[0], "--errors="):
			return nil, usagef("--errors supports only json")
		default:
			return result, nil
		}
	}
	return result, nil
}

func boundedErrorMessage(err error) string {
	if err == nil {
		return "operation failed"
	}
	message := strings.ToValidUTF8(strings.TrimSpace(err.Error()), "?")
	message = strings.ReplaceAll(message, "\x00", "?")
	if message == "" {
		return "operation failed"
	}
	const maximum = 2048
	if len([]byte(message)) <= maximum {
		return message
	}
	raw := []byte(message)[:maximum]
	for !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return string(raw)
}
