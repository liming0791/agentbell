package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPServerRunsOnLoopbackAndShutsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := HTTPServer{
		Address: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.WriteHeader(http.StatusNoContent)
		}),
		Listen: func(network, address string) (net.Listener, error) {
			if network != "tcp" || address != "127.0.0.1:0" {
				t.Fatalf("listen %q %q", network, address)
			}
			return listener, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()

	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay HTTP server did not shut down")
	}
}

func TestHTTPServerRejectsUnsafeListenerConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		server HTTPServer
	}{
		{"missing address", HTTPServer{}},
		{"wildcard without TLS", HTTPServer{
			Address: "0.0.0.0:18443",
			Handler: http.NotFoundHandler(),
		}},
		{"partial TLS", HTTPServer{
			Address:  "127.0.0.1:18443",
			CertFile: "/tmp/cert.pem",
			Handler:  http.NotFoundHandler(),
		}},
		{"ephemeral production port", HTTPServer{
			Address: "127.0.0.1:0",
			Handler: http.NotFoundHandler(),
		}},
		{"missing handler", HTTPServer{Address: "127.0.0.1:18443"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.server.Run(context.Background()); err == nil {
				t.Fatal("unsafe relay listener started")
			}
		})
	}
}

func TestHTTPServerPropagatesListenErrorWithoutSensitiveDetails(t *testing.T) {
	sentinel := errors.New("listen failed")
	server := HTTPServer{
		Address: "127.0.0.1:18443",
		Handler: http.NotFoundHandler(),
		Listen: func(string, string) (net.Listener, error) {
			return nil, sentinel
		},
	}
	err := server.Run(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret-body") {
		t.Fatalf("error leaked request material: %v", err)
	}
}

func TestHTTPServerRejectsUnexpectedServeFailure(t *testing.T) {
	listener := &failingListener{err: errors.New("accept failed")}
	server := HTTPServer{
		Address: "127.0.0.1:18443",
		Handler: http.NotFoundHandler(),
		Listen: func(string, string) (net.Listener, error) {
			return listener, nil
		},
	}
	if err := server.Run(context.Background()); err == nil {
		t.Fatal("serve failure was hidden")
	}
}

type failingListener struct {
	err error
}

func (listener *failingListener) Accept() (net.Conn, error) {
	return nil, listener.err
}

func (listener *failingListener) Close() error {
	return nil
}

func (listener *failingListener) Addr() net.Addr {
	return fakeAddr("127.0.0.1:18443")
}

type fakeAddr string

func (address fakeAddr) Network() string { return "tcp" }
func (address fakeAddr) String() string  { return string(address) }

var _ io.Closer = (*failingListener)(nil)
