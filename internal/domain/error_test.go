package domain

import (
	"errors"
	"testing"
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind ErrorKind
		want int
	}{
		{Unexpected, 1},
		{Usage, 2},
		{NotFound, 3},
		{Conflict, 4},
		{Storage, 5},
		{Confirmation, 6},
	}
	for _, test := range tests {
		err := NewError(test.kind, "test", "test", nil)
		if got := ExitCode(err); got != test.want {
			t.Fatalf("ExitCode(kind %d) = %d, want %d", test.kind, got, test.want)
		}
	}
}

func TestPublicErrorHidesUnexpectedCause(t *testing.T) {
	t.Parallel()

	err := errors.New("sensitive implementation detail")
	public := PublicError(err)
	if public.Code != "internal_error" || public.Message != "unexpected operational failure" {
		t.Fatalf("PublicError() = %#v", public)
	}
	if !errors.Is(public, err) {
		t.Fatal("PublicError() did not retain the cause")
	}
}
