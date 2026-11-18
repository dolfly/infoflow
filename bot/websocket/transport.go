package websocket

import (
	"context"
	"net/http"
	"sync"

	coderws "github.com/coder/websocket"
)

// Transport is the byte-oriented boundary owned by the InfoFlow session.
type Transport interface {
	Write(context.Context, []byte) error
	Close() error
}

// TransportCallbacks only enqueue generation-tagged events in Session; they
// never mutate session state or call user handlers directly.
type TransportCallbacks struct {
	OnMessage func([]byte)
	OnClose   func()
	OnError   func()
}

// DialOptions is intentionally small and inspectable by deterministic fakes.
type DialOptions struct {
	Header              http.Header
	CompressionDisabled bool
}

// Dialer creates an RFC 6455 transport. Production uses coder/websocket;
// tests can provide a deterministic fake without networking.
type Dialer interface {
	Dial(context.Context, string, DialOptions, TransportCallbacks) (Transport, error)
}

// DialerFunc adapts a function to Dialer.
type DialerFunc func(context.Context, string, DialOptions, TransportCallbacks) (Transport, error)

func (function DialerFunc) Dial(ctx context.Context, endpointURL string, options DialOptions, callbacks TransportCallbacks) (Transport, error) {
	return function(ctx, endpointURL, options, callbacks)
}

type coderDialer struct {
	httpClient *http.Client
}

type coderConnection interface {
	Read(context.Context) (coderws.MessageType, []byte, error)
	Write(context.Context, coderws.MessageType, []byte) error
	CloseNow() error
}

// NewCoderDialer creates the production transport adapter. Compression is
// always disabled by the adapter as well as asserted in DialOptions.
func NewCoderDialer(httpClient *http.Client) Dialer {
	return &coderDialer{httpClient: httpClient}
}

func (dialer *coderDialer) Dial(ctx context.Context, endpointURL string, options DialOptions, callbacks TransportCallbacks) (Transport, error) {
	connection, _, err := coderws.Dial(ctx, endpointURL, &coderws.DialOptions{
		HTTPClient:      dialer.httpClient,
		HTTPHeader:      options.Header.Clone(),
		CompressionMode: coderws.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	transport := &coderTransport{connection: connection, callbacks: callbacks, done: make(chan struct{})}
	go transport.readLoop()
	return transport, nil
}

type coderTransport struct {
	connection coderConnection
	callbacks  TransportCallbacks
	done       chan struct{}
	closeOnce  sync.Once
}

func (transport *coderTransport) Write(ctx context.Context, payload []byte) error {
	return transport.connection.Write(ctx, coderws.MessageBinary, payload)
}

func (transport *coderTransport) Close() error {
	var err error
	transport.closeOnce.Do(func() {
		close(transport.done)
		err = transport.connection.CloseNow()
	})
	return err
}

func (transport *coderTransport) readLoop() {
	for {
		messageType, payload, err := transport.connection.Read(context.Background())
		if err != nil {
			select {
			case <-transport.done:
				return
			default:
			}
			status := coderws.CloseStatus(err)
			if status == coderws.StatusNormalClosure || status == coderws.StatusGoingAway ||
				status == coderws.StatusNoStatusRcvd {
				if transport.callbacks.OnClose != nil {
					transport.callbacks.OnClose()
				}
			} else if transport.callbacks.OnError != nil {
				transport.callbacks.OnError()
			}
			return
		}
		if messageType != coderws.MessageBinary {
			if transport.callbacks.OnError != nil {
				transport.callbacks.OnError()
			}
			_ = transport.Close()
			return
		}
		if transport.callbacks.OnMessage != nil {
			transport.callbacks.OnMessage(payload)
		}
	}
}
