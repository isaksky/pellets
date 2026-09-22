package webui

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"pellets/internal/app"
)

func TestListenLoopbackPortSelection(t *testing.T) {
	busy := &net.OpError{Op: "listen", Net: "tcp4", Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRINUSE}}
	for _, test := range []struct {
		name      string
		port      uint16
		retry     bool
		failures  int
		failure   error
		want      []string
		wantError bool
	}{
		{name: "default available", port: 7419, retry: true, want: []string{"7419"}},
		{name: "skip occupied ports", port: 7419, retry: true, failures: 2, failure: busy, want: []string{"7419", "7420", "7421"}},
		{name: "explicit port strict", port: 7419, failures: 1, failure: busy, want: []string{"7419"}, wantError: true},
		{name: "OS selected", port: 0, want: []string{"0"}},
		{name: "zero never increments", port: 0, retry: true, failures: 1, failure: busy, want: []string{"0"}, wantError: true},
		{name: "permission denied", port: 7419, retry: true, failures: 1, failure: syscall.EACCES, want: []string{"7419"}, wantError: true},
		{name: "range exhausted", port: 65534, retry: true, failures: 2, failure: busy, want: []string{"65534", "65535"}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts []string
			runner := Runner{Listen: func(network, address string) (net.Listener, error) {
				host, port, err := net.SplitHostPort(address)
				if err != nil || network != "tcp4" || host != "127.0.0.1" {
					t.Fatalf("unexpected listener: %s %s (%v)", network, address, err)
				}
				attempts = append(attempts, port)
				if len(attempts) <= test.failures {
					return nil, test.failure
				}
				return net.Listen("tcp4", "127.0.0.1:0")
			}}
			listener, err := runner.listenLoopback(context.Background(), test.port, test.retry)
			if listener != nil {
				listener.Close()
			}
			if test.wantError {
				if !errors.Is(err, test.failure) || !strings.Contains(err.Error(), "--port") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(attempts, test.want) {
				t.Fatalf("attempts = %v, want %v", attempts, test.want)
			}
		})
	}
}

func TestListenLoopbackRetriesRealOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	if port == 65535 {
		t.Skip("OS selected the last port")
	}
	listener, err := (Runner{}).listenLoopback(context.Background(), uint16(port), true)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if selected := listener.Addr().(*net.TCPAddr); !selected.IP.IsLoopback() || selected.Port <= port {
		t.Fatalf("selected %v after occupied port %d", selected, port)
	}
}

func TestListenLoopbackCancellationStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	runner := Runner{Listen: func(string, string) (net.Listener, error) {
		calls++
		cancel()
		return nil, syscall.EADDRINUSE
	}}
	_, err := runner.listenLoopback(ctx, 7419, true)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}

func TestRunnerListenFailureDoesNotInitializeApplication(t *testing.T) {
	runner := Runner{
		OpenApplication: func(context.Context, string, string, string) (*app.WebApplication, error) {
			t.Fatal("application opened before listener succeeded")
			return nil, nil
		},
		OpenMonitor: func(context.Context, string) (Monitor, error) {
			t.Fatal("monitor opened before listener succeeded")
			return nil, nil
		},
		OpenSupervisor: func(context.Context) *app.ExecutionSupervisor {
			t.Fatal("supervisor opened before listener succeeded")
			return nil
		},
		Listen: func(string, string) (net.Listener, error) { return nil, syscall.EACCES },
	}
	err := runner.Run(context.Background(), Options{Port: 7419, RetryPort: true})
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestRunnerStartupFailureReleasesPort(t *testing.T) {
	want := errors.New("application unavailable")
	var port int
	runner := Runner{
		OpenApplication: func(context.Context, string, string, string) (*app.WebApplication, error) { return nil, want },
		OpenMonitor:     func(context.Context, string) (Monitor, error) { return nil, nil },
		Listen: func(network, address string) (net.Listener, error) {
			listener, err := net.Listen(network, address)
			if err == nil {
				port = listener.Addr().(*net.TCPAddr).Port
			}
			return listener, err
		},
	}
	if err := runner.Run(context.Background(), Options{}); !errors.Is(err, want) {
		t.Fatalf("Run error = %v", err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("startup failure left port bound: %v", err)
	}
	listener.Close()
}
