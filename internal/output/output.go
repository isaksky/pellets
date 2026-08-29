// Package output implements the public JSON envelope and human rendering boundary.
package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"pellets/internal/domain"
)

const SchemaVersion = 1

// Renderer writes a successful command result.
type Renderer interface {
	Render(w io.Writer, command string, data any) error
}

// HumanRenderable is implemented by result values that have a human presentation.
type HumanRenderable interface {
	RenderHuman(w io.Writer) error
}

// JSONRenderer emits the stable, versioned success envelope.
type JSONRenderer struct {
	Pretty bool
}

func (r JSONRenderer) Render(w io.Writer, command string, data any) error {
	envelope := successEnvelope{
		SchemaVersion: SchemaVersion,
		Command:       command,
		Data:          data,
	}
	return encodeJSON(w, envelope, r.Pretty)
}

// HumanRenderer delegates presentation to the application result type.
type HumanRenderer struct{}

func (HumanRenderer) Render(w io.Writer, _ string, data any) error {
	human, ok := data.(HumanRenderable)
	if !ok {
		return fmt.Errorf("result type %T has no human renderer", data)
	}
	var buffer bytes.Buffer
	if err := human.RenderHuman(&buffer); err != nil {
		return err
	}
	return write(w, buffer.Bytes())
}

// WriteError emits the stable JSON error envelope. Errors remain JSON in human mode.
func WriteError(w io.Writer, err error) error {
	public := domain.PublicError(err)
	envelope := errorEnvelope{SchemaVersion: SchemaVersion}
	envelope.Error.Code = public.Code
	envelope.Error.Message = public.Message
	envelope.Error.Details = public.Details

	return encodeJSON(w, envelope, false)
}

// IsWriteFailure reports whether rendering failed while writing completed output.
// The CLI uses this to handle broken pipes quietly.
func IsWriteFailure(err error) bool {
	var failure *writeFailure
	return errors.As(err, &failure)
}

type writeFailure struct {
	cause error
}

func (e *writeFailure) Error() string { return e.cause.Error() }
func (e *writeFailure) Unwrap() error { return e.cause }

func encodeJSON(w io.Writer, value any, pretty bool) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return write(w, buffer.Bytes())
}

func write(w io.Writer, data []byte) error {
	written, err := w.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return &writeFailure{cause: err}
	}
	return nil
}

type successEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	Data          any    `json:"data"`
}

type errorEnvelope struct {
	SchemaVersion int `json:"schema_version"`
	Error         struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
}
