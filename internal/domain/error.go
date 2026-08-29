// Package domain contains the stable errors shared by the application layers.
package domain

import (
	"errors"
	"fmt"
)

// ErrorKind determines the process exit code for an Error.
type ErrorKind uint8

const (
	Unexpected ErrorKind = iota
	Usage
	NotFound
	Conflict
	Storage
	Confirmation
)

// Error is a failure that can safely cross the CLI boundary.
// Cause is retained for diagnostics but is never included in normal output.
type Error struct {
	Kind    ErrorKind
	Code    string
	Message string
	Details map[string]any
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewError constructs a public typed error.
func NewError(kind ErrorKind, code, message string, details map[string]any) *Error {
	return &Error{Kind: kind, Code: code, Message: message, Details: details}
}

// WrapError constructs a public typed error while retaining its internal cause.
func WrapError(kind ErrorKind, code, message string, details map[string]any, cause error) *Error {
	return &Error{Kind: kind, Code: code, Message: message, Details: details, Cause: cause}
}

// PublicError returns err when it is typed, or a safe unexpected error otherwise.
func PublicError(err error) *Error {
	var public *Error
	if errors.As(err, &public) {
		return public
	}
	return WrapError(Unexpected, "internal_error", "unexpected operational failure", nil, err)
}

// ExitCode maps a typed error kind to the public CLI exit contract.
func ExitCode(err error) int {
	switch PublicError(err).Kind {
	case Usage:
		return 2
	case NotFound:
		return 3
	case Conflict:
		return 4
	case Storage:
		return 5
	case Confirmation:
		return 6
	default:
		return 1
	}
}
