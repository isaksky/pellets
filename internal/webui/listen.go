package webui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"syscall"
)

func (runner Runner) listenLoopback(ctx context.Context, port uint16, retry bool) (net.Listener, error) {
	listen := runner.Listen
	if listen == nil {
		listen = net.Listen
	}
	// Use an int so incrementing 65535 cannot wrap around to port zero.
	for candidate := int(port); ; candidate++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(candidate))
		listener, err := listen("tcp4", address)
		if err == nil {
			return listener, nil
		}
		if !retry || port == 0 || !addressInUse(err) {
			return nil, fmt.Errorf("listen on %s (choose another port with --port): %w", address, err)
		}
		if candidate == 65535 {
			return nil, fmt.Errorf("no available loopback port from %d through 65535 (choose another port with --port): %w", port, err)
		}
	}
}

func addressInUse(err error) bool {
	// Winsock reports WSAEADDRINUSE (10048), rather than EADDRINUSE.
	return errors.Is(err, syscall.EADDRINUSE) ||
		(runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10048)))
}
