package remote

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

func TestRemoteValueRedactionAndBuilderEdges(t *testing.T) {
	spec, err := BuildPullCommand(validRemoteConfig("container"))
	if err != nil {
		t.Fatal(err)
	}
	if spec.GoString() != spec.String() {
		t.Fatal("command GoString differs from redacted String")
	}
	config := validRemoteConfig("container")
	config.Connector.Type = "unknown"
	if _, err := BuildPullCommand(config); !errors.Is(err, ErrInvalidRemoteConfig) {
		t.Fatalf("unknown connector error = %v", err)
	}
}

func TestDrainOutboxEdgeValidationAndLimit(t *testing.T) {
	reader, writer := io.Pipe()
	if _, err := DrainOutbox(
		nil,
		nil,
		reader,
		writer,
		DrainOptions{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("nil context error = %v", err)
	}
	_ = reader.Close()
	_ = writer.Close()

	outbox, err := relay.OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []DrainOptions{
		{Lease: -1},
		{MaxItems: -1},
		{Backoff: []time.Duration{0}},
	} {
		reader, writer := io.Pipe()
		_, drainErr := DrainOutbox(
			context.Background(),
			outbox,
			reader,
			writer,
			options,
		)
		_ = reader.Close()
		_ = writer.Close()
		if !errors.Is(drainErr, ErrInvalidDrain) {
			t.Fatalf("options=%#v error=%v", options, drainErr)
		}
	}

	emptyReader, emptyWriter := io.Pipe()
	count, err := DrainOutbox(
		context.Background(),
		outbox,
		emptyReader,
		emptyWriter,
		DrainOptions{},
	)
	if err != nil || count != 0 {
		t.Fatalf("empty drain count=%d error=%v", count, err)
	}

	request := validForwardRequest(t)
	now := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	if _, _, err := outbox.Enqueue(
		request.ExactBody,
		request.Signature,
		now,
	); err != nil {
		t.Fatal(err)
	}
	drainInput, hostOutput := io.Pipe()
	hostInput, drainOutput := io.Pipe()
	done := make(chan error, 1)
	go func() {
		request, err := relay.ReadForwardRequest(hostInput)
		if err == nil {
			err = relay.WriteForwardACK(hostOutput, relay.ForwardACK{
				ItemID:       request.ItemID,
				DeliveryKey:  request.DeliveryKey,
				BodyDigest:   request.BodyDigest,
				ReceiptID:    request.ItemID,
				LocalQueueID: "queue-limit",
				Durable:      true,
				CommittedAt:  now,
			})
		}
		done <- err
	}()
	count, err = DrainOutbox(
		context.Background(),
		outbox,
		drainInput,
		drainOutput,
		DrainOptions{
			Now:      func() time.Time { return now },
			MaxItems: 1,
		},
	)
	if count != 1 || !errors.Is(err, ErrDrainLimit) {
		t.Fatalf("limited drain count=%d error=%v", count, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPullerProtocolTimeoutAndDependencyEdges(t *testing.T) {
	config := validRemoteConfig("wsl")
	if _, err := (Puller{Ingress: &ingressStub{}}).Pull(
		nil,
		config,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("nil context error = %v", err)
	}
	for _, puller := range []Puller{
		{Ingress: &ingressStub{}, Timeout: -1},
		{Ingress: &ingressStub{}, MaxFrames: -1},
	} {
		if _, err := puller.Pull(
			context.Background(),
			config,
		); !errors.Is(err, ErrInvalidPuller) {
			t.Fatalf("puller=%#v error=%v", puller, err)
		}
	}
	if _, err := (Puller{Ingress: &ingressStub{}}).Pull(
		context.Background(),
		validRemoteConfig("https"),
	); !errors.Is(err, ErrNotPullConnector) {
		t.Fatalf("HTTPS pull error = %v", err)
	}
	if _, err := (Puller{
		Runner:  &fakeRunner{},
		Ingress: &ingressStub{},
	}).Pull(context.Background(), config); !errors.Is(err, ErrConnectorStart) {
		t.Fatalf("nil process error = %v", err)
	}

	t.Run("deadline closes blocked read", func(t *testing.T) {
		process, _, _ := newFakeProcess()
		_, err := (Puller{
			Runner:  &fakeRunner{process: process},
			Ingress: &ingressStub{},
			Timeout: 10 * time.Millisecond,
		}).Pull(context.Background(), config)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("malformed frame", func(t *testing.T) {
		process, remoteOutput, _ := newFakeProcess()
		go func() {
			_, _ = io.WriteString(remoteOutput, "not-a-frame")
			_ = remoteOutput.Close()
			process.waitResult <- nil
		}()
		_, err := (Puller{
			Runner:  &fakeRunner{process: process},
			Ingress: &ingressStub{},
			Timeout: time.Second,
		}).Pull(context.Background(), config)
		if !errors.Is(err, ErrConnectorProtocol) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid durable ACK binding", func(t *testing.T) {
		request := validForwardRequest(t)
		process, remoteOutput, _ := newFakeProcess()
		go func() {
			_ = relay.WriteForwardRequest(remoteOutput, request)
			process.waitResult <- nil
		}()
		_, err := (Puller{
			Runner:  &fakeRunner{process: process},
			Ingress: &ingressStub{receiptID: "wrong-receipt"},
			Timeout: time.Second,
		}).Pull(context.Background(), config)
		if !errors.Is(err, ErrConnectorProtocol) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("ACK write failure", func(t *testing.T) {
		request := validForwardRequest(t)
		process, remoteOutput, remoteInput := newFakeProcess()
		go func() {
			_ = remoteInput.Close()
			_ = relay.WriteForwardRequest(remoteOutput, request)
			process.waitResult <- nil
		}()
		_, err := (Puller{
			Runner:  &fakeRunner{process: process},
			Ingress: &ingressStub{receiptID: request.ItemID},
			Timeout: time.Second,
		}).Pull(context.Background(), config)
		if !errors.Is(err, ErrConnectorProtocol) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("zero commit clock", func(t *testing.T) {
		request := validForwardRequest(t)
		process, remoteOutput, _ := newFakeProcess()
		go func() {
			_ = relay.WriteForwardRequest(remoteOutput, request)
			process.waitResult <- nil
		}()
		_, err := (Puller{
			Runner:  &fakeRunner{process: process},
			Ingress: &ingressStub{receiptID: request.ItemID},
			Now:     func() time.Time { return time.Time{} },
			Timeout: time.Second,
		}).Pull(context.Background(), config)
		if !errors.Is(err, ErrConnectorProtocol) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestExecRunnerAndProcessCloseEdges(t *testing.T) {
	if _, err := (ExecRunner{}).Start(
		context.Background(),
		CommandSpec{Executable: "/definitely/not/agentbell"},
	); !errors.Is(err, ErrConnectorStart) {
		t.Fatalf("missing executable error = %v", err)
	}
}

func TestHTTPSDecodeAndLifecycleEdges(t *testing.T) {
	for _, body := range []string{
		``,
		`[]`,
		`{"unknown":true}`,
		`{"receiptId":"a","localQueueId":"q"}`,
		`{"receiptId":1,"localQueueId":"q","duplicate":false}`,
		`{"receiptId":"a","localQueueId":"q","duplicate":false} {}`,
	} {
		if _, err := decodeIngressACK([]byte(body)); !errors.Is(
			err,
			ErrHTTPSResponse,
		) {
			t.Fatalf("body=%q error=%v", body, err)
		}
	}
	if err := (*HTTPSTransport)(nil).Close(); err != nil {
		t.Fatal(err)
	}

	config := validRemoteConfig("https")
	config.Runtime = "vendor-cloud"
	config.Connector.Type = "vendor-cloud"
	config.Connector.HTTPS = nil
	config.Connector.VendorCloud = &remoteconfig.VendorCloudConnector{
		Provider:   "kimi",
		Capability: "unverified",
		Endpoint:   "https://cloud.example.com/hook",
	}
	if _, err := NewHTTPSTransport(config, HTTPSOptions{}); !errors.Is(
		err,
		ErrVendorCloudUnsupported,
	) {
		t.Fatalf("vendor connector error = %v", err)
	}
}

func TestHTTPSTransportTimeoutAndRequestMismatch(t *testing.T) {
	request := validForwardRequest(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		time.Sleep(50 * time.Millisecond)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	roots := rootsForServer(t, server)
	config := validRemoteConfig("https")
	config.Connector.HTTPS.Endpoint = server.URL + "/v1/events"
	transport, err := NewHTTPSTransport(config, HTTPSOptions{
		RootCAs: roots,
		Timeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if _, err := transport.Send(
		context.Background(),
		request,
	); !errors.Is(err, ErrHTTPSRequest) {
		t.Fatalf("timeout error = %v", err)
	}

	request.Signature.Target = "/different"
	if _, err := transport.Send(
		context.Background(),
		request,
	); !errors.Is(err, ErrHTTPSRequest) {
		t.Fatalf("target mismatch error = %v", err)
	}
	if transport.GoString() != transport.String() {
		t.Fatal("HTTPS GoString differs from redacted String")
	}
}

func TestHTTPSTransportDoesNotForwardSignedRequestAcrossRedirect(t *testing.T) {
	request := validForwardRequest(t)
	var redirectFollowed atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		httpRequest *http.Request,
	) {
		if httpRequest.URL.Path == "/other" {
			redirectFollowed.Store(true)
			response.WriteHeader(http.StatusAccepted)
			return
		}
		http.Redirect(response, httpRequest, "/other", http.StatusFound)
	}))
	defer server.Close()
	config := validRemoteConfig("https")
	config.Connector.HTTPS.Endpoint = server.URL + "/v1/events"
	transport, err := NewHTTPSTransport(config, HTTPSOptions{
		RootCAs: rootsForServer(t, server),
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if _, err := transport.Send(
		context.Background(),
		request,
	); !errors.Is(err, ErrHTTPSResponse) {
		t.Fatalf("redirect error = %v", err)
	}
	if redirectFollowed.Load() {
		t.Fatal("signed request was forwarded across redirect")
	}
}

func rootsForServer(
	t *testing.T,
	server *httptest.Server,
) *x509.CertPool {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return roots
}
