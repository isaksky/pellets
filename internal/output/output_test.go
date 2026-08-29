package output

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"pellets/internal/domain"
)

func TestWriteErrorDoesNotExposeCause(t *testing.T) {
	t.Parallel()

	public := domain.WrapError(
		domain.Storage,
		"database_failure",
		"database operation failed",
		nil,
		errors.New("SELECT secret FROM internal_table"),
	)
	var output bytes.Buffer
	if err := WriteError(&output, public); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "SELECT") {
		t.Fatalf("error output exposed cause: %s", output.String())
	}
}

func TestJSONEncodingFailureWritesNothing(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := (JSONRenderer{}).Render(&output, "bad", make(chan int))
	if err == nil {
		t.Fatal("Render() unexpectedly succeeded")
	}
	if output.Len() != 0 {
		t.Fatalf("Render() wrote partial JSON: %q", output.String())
	}
}

func TestWriteFailureIsIdentifiable(t *testing.T) {
	t.Parallel()

	err := (JSONRenderer{}).Render(failingWriter{}, "status", struct{}{})
	if !IsWriteFailure(err) {
		t.Fatalf("IsWriteFailure(%v) = false", err)
	}
}

func TestFormatTimestampIsStableUTC(t *testing.T) {
	t.Parallel()

	value := time.Date(2026, time.August, 28, 20, 1, 2, 345000000, time.FixedZone("offset", -6*60*60))
	if got, want := FormatTimestamp(value), "2026-08-29T02:01:02.345Z"; got != want {
		t.Fatalf("FormatTimestamp() = %q, want %q", got, want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
