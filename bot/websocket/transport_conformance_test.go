package websocket

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
)

type scriptedCoderConnection struct {
	readType coderws.MessageType
	readErr  error
	closed   atomic.Bool
}

func (connection *scriptedCoderConnection) Read(context.Context) (coderws.MessageType, []byte, error) {
	return connection.readType, nil, connection.readErr
}

func (*scriptedCoderConnection) Write(context.Context, coderws.MessageType, []byte) error {
	return nil
}

func (connection *scriptedCoderConnection) CloseNow() error {
	connection.closed.Store(true)
	return nil
}

func TestCoderTransportClassifiesReadCloseAndErrorCallbacks(t *testing.T) {
	tests := []struct {
		name      string
		readErr   error
		wantClose bool
		wantError bool
	}{
		{name: "normal close", readErr: coderws.CloseError{Code: coderws.StatusNormalClosure}, wantClose: true},
		{name: "going away", readErr: coderws.CloseError{Code: coderws.StatusGoingAway}, wantClose: true},
		{name: "no status", readErr: coderws.CloseError{Code: coderws.StatusNoStatusRcvd}, wantClose: true},
		{name: "protocol close", readErr: coderws.CloseError{Code: coderws.StatusProtocolError}, wantError: true},
		{name: "network read", readErr: errors.New("fixture read failure"), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			closed := make(chan struct{}, 1)
			failed := make(chan struct{}, 1)
			transport := &coderTransport{
				connection: &scriptedCoderConnection{readErr: test.readErr},
				callbacks: TransportCallbacks{
					OnClose: func() { closed <- struct{}{} },
					OnError: func() { failed <- struct{}{} },
				},
				done: make(chan struct{}),
			}
			transport.readLoop()
			select {
			case <-closed:
				if !test.wantClose {
					t.Fatal("unexpected OnClose callback")
				}
			default:
				if test.wantClose {
					t.Fatal("missing OnClose callback")
				}
			}
			select {
			case <-failed:
				if !test.wantError {
					t.Fatal("unexpected OnError callback")
				}
			default:
				if test.wantError {
					t.Fatal("missing OnError callback")
				}
			}
		})
	}
}

func TestCoderTransportRejectsNonBinaryFrameAsError(t *testing.T) {
	failed := make(chan struct{}, 1)
	connection := &scriptedCoderConnection{readType: coderws.MessageText}
	transport := &coderTransport{
		connection: connection,
		callbacks:  TransportCallbacks{OnError: func() { failed <- struct{}{} }},
		done:       make(chan struct{}),
	}
	transport.readLoop()
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("missing OnError callback")
	}
	if !connection.closed.Load() {
		t.Fatal("non-binary transport was not closed")
	}
}
