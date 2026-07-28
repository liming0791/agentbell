package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ListenFunc func(network, address string) (net.Listener, error)

type HTTPServer struct {
	Address   string
	CertFile  string
	KeyFile   string
	SSHTunnel bool
	Handler   http.Handler
	Listen    ListenFunc
}

func (server HTTPServer) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("relay server context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := server.validate(); err != nil {
		return err
	}
	listen := server.Listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp", server.Address)
	if err != nil {
		return fmt.Errorf("start relay listener: %w", err)
	}
	httpServer := &http.Server{
		Handler:           server.Handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	serveResult := make(chan error, 1)
	go func() {
		if server.CertFile != "" {
			serveResult <- httpServer.ServeTLS(
				listener,
				server.CertFile,
				server.KeyFile,
			)
			return
		}
		serveResult <- httpServer.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("relay listener stopped: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			_ = httpServer.Close()
			return fmt.Errorf("shut down relay listener: %w", err)
		}
		err := <-serveResult
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("relay listener stopped during shutdown: %w", err)
		}
		return nil
	}
}

func (server HTTPServer) validate() error {
	if server.Handler == nil {
		return errors.New("relay HTTP handler is required")
	}
	host, portText, err := net.SplitHostPort(server.Address)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("relay listen address must use host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil ||
		port < 0 ||
		port > 65535 ||
		(port == 0 && server.Listen == nil) {
		return errors.New("relay listener port is invalid")
	}
	if (server.CertFile == "") != (server.KeyFile == "") {
		return errors.New("relay TLS certificate and key must be supplied together")
	}
	if server.CertFile != "" &&
		(!filepath.IsAbs(server.CertFile) ||
			!filepath.IsAbs(server.KeyFile) ||
			filepath.Clean(server.CertFile) != server.CertFile ||
			filepath.Clean(server.KeyFile) != server.KeyFile ||
			server.CertFile == server.KeyFile) {
		return errors.New("relay TLS files must be distinct clean absolute paths")
	}
	if !relayLoopbackHost(host) &&
		server.CertFile == "" &&
		!server.SSHTunnel {
		return errors.New(
			"non-loopback relay listener requires TLS or an explicit SSH tunnel",
		)
	}
	return nil
}

func relayLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
