// Package webui implements the foreground, loopback-only Pellets inspector.
package webui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"pellets/internal/app"
)

const (
	defaultMonitorInterval = 300 * time.Millisecond
	defaultCoalesceDelay   = 120 * time.Millisecond
	shutdownTimeout        = 5 * time.Second
)

type Monitor interface {
	DataVersion(context.Context) (int64, error)
	Close() error
}

type ApplicationOpener func(context.Context, string, string, string) (*app.WebApplication, error)
type MonitorOpener func(context.Context, string) (Monitor, error)
type BrowserOpener func(string) error

// Runner owns process-lifetime resources and is injectable at every platform
// or persistence boundary used by command tests.
type Runner struct {
	OpenApplication ApplicationOpener
	OpenMonitor     MonitorOpener
	OpenBrowser     BrowserOpener
	Listen          func(network, address string) (net.Listener, error)
	MonitorInterval time.Duration
	CoalesceDelay   time.Duration
}

type Options struct {
	DatabaseRoot     string
	DatabasePath     string
	WorkingDirectory string
	InitialProject   string
	Port             uint16
	NoOpen           bool
	Stdout           io.Writer
	Stderr           io.Writer
}

func (runner Runner) Run(ctx context.Context, options Options) error {
	if runner.OpenApplication == nil || runner.OpenMonitor == nil {
		return errors.New("web runner is not configured")
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	application, err := runner.OpenApplication(ctx, options.DatabaseRoot, options.DatabasePath, options.WorkingDirectory)
	if err != nil {
		return err
	}
	defer application.Close()
	monitor, err := runner.OpenMonitor(ctx, options.DatabasePath)
	if err != nil {
		return err
	}
	defer monitor.Close()

	listen := runner.Listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(options.Port))))
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}
	defer listener.Close()
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !tcpAddress.IP.IsLoopback() {
		return errors.New("listener did not resolve to a loopback TCP address")
	}
	host := net.JoinHostPort("127.0.0.1", strconv.Itoa(tcpAddress.Port))
	baseURL := "http://" + host

	csrf, err := randomCapability()
	if err != nil {
		return fmt.Errorf("create CSRF capability: %w", err)
	}
	hub := newEventHub()
	interval := runner.MonitorInterval
	if interval <= 0 {
		interval = defaultMonitorInterval
	}
	coalesce := runner.CoalesceDelay
	if coalesce <= 0 {
		coalesce = defaultCoalesceDelay
	}
	monitorContext, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	monitorDone := make(chan struct{})

	handler, err := newHandler(application, hub, handlerConfig{
		Host: host, Origin: baseURL, CSRF: csrf, InitialProject: options.InitialProject,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	serveErrors := make(chan error, 1)
	go func() {
		defer close(monitorDone)
		runDataVersionMonitor(monitorContext, monitor, hub, interval, coalesce)
	}()
	go func() { serveErrors <- httpServer.Serve(listener) }()

	// Listen has succeeded and Serve is scheduled before the URL is printed or
	// a browser can be launched.
	if _, err := fmt.Fprintln(options.Stdout, baseURL); err != nil {
		_ = httpServer.Close()
		return err
	}
	if !options.NoOpen && runner.OpenBrowser != nil {
		if err := runner.OpenBrowser(baseURL); err != nil {
			fmt.Fprintf(options.Stderr, "warning: could not open the default browser: %v; open %s manually\n", err, baseURL)
		}
	}

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		shutdownErr := httpServer.Shutdown(shutdownContext)
		cancel()
		if shutdownErr != nil {
			_ = httpServer.Close()
		}
		cancelMonitor()
		<-monitorDone
		return nil
	case err := <-serveErrors:
		cancelMonitor()
		<-monitorDone
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve local web interface: %w", err)
	}
}

func randomCapability() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

type eventHub struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
	changed chan struct{}
}

func newEventHub() *eventHub {
	return &eventHub{clients: make(map[chan struct{}]struct{}), changed: make(chan struct{}, 1)}
}

func (hub *eventHub) subscribe() (<-chan struct{}, func()) {
	client := make(chan struct{}, 1)
	hub.mu.Lock()
	hub.clients[client] = struct{}{}
	hub.mu.Unlock()
	hub.signalClientChange()
	var once sync.Once
	return client, func() {
		once.Do(func() {
			hub.mu.Lock()
			delete(hub.clients, client)
			hub.mu.Unlock()
			hub.signalClientChange()
		})
	}
}

func (hub *eventHub) signalClientChange() {
	select {
	case hub.changed <- struct{}{}:
	default:
	}
}

func (hub *eventHub) clientCount() int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.clients)
}

func (hub *eventHub) broadcast() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for client := range hub.clients {
		select {
		case client <- struct{}{}:
		default:
		}
	}
}

func runDataVersionMonitor(ctx context.Context, monitor Monitor, hub *eventHub, interval, coalesce time.Duration) {
	var baseline int64
	var haveBaseline bool
	var timer *time.Timer
	var timerChannel <-chan time.Time
	stopCoalescer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerChannel = nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer stopCoalescer()

	for {
		if hub.clientCount() == 0 {
			haveBaseline = false
			stopCoalescer()
			select {
			case <-ctx.Done():
				return
			case <-hub.changed:
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-hub.changed:
			continue
		case <-timerChannel:
			timerChannel = nil
			hub.broadcast()
		case <-ticker.C:
			version, err := monitor.DataVersion(ctx)
			if err != nil {
				continue
			}
			if !haveBaseline {
				baseline = version
				haveBaseline = true
				continue
			}
			if version == baseline {
				continue
			}
			baseline = version
			if timerChannel == nil {
				if timer == nil {
					timer = time.NewTimer(coalesce)
				} else {
					timer.Reset(coalesce)
				}
				timerChannel = timer.C
			}
		}
	}
}
