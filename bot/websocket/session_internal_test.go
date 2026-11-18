package websocket

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeSourceID(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "blank", value: "", want: "go_sdk"},
		{name: "whitespace", value: " \t ", want: "go_sdk"},
		{name: "explicit prefix", value: "myapp", want: "myapp_go_sdk"},
		{name: "already suffixed idempotent", value: "myapp_go_sdk", want: "myapp_go_sdk"},
		{name: "default value idempotent", value: "go_sdk", want: "go_sdk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSourceID(tc.value); got != tc.want {
				t.Errorf("normalizeSourceID(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestAdvanceGenerationPurgesOldReceiveAndWriterQueues(t *testing.T) {
	completion := make(chan error, 1)
	session := &Session{
		receiveQ: make(chan receiveEvent, 2),
		writerQ:  make(chan writeRequest, 2),
	}
	session.state.Store(StateConnected)
	session.generation = 7
	session.generationState.Store(7)
	session.receiveQ <- receiveEvent{generation: 7, payload: []byte("old-receive")}
	session.receiveQ <- receiveEvent{generation: 99, payload: []byte("retained-receive")}
	session.writerQ <- writeRequest{generation: 7, completion: completion}
	session.writerQ <- writeRequest{generation: 99, kind: writeACK}

	session.advanceGeneration()

	if got := len(session.receiveQ); got != 1 {
		t.Fatalf("retained receive queue length = %d, want 1", got)
	}
	if event := <-session.receiveQ; event.generation != 99 {
		t.Fatalf("retained receive generation = %d, want 99", event.generation)
	}
	if got := len(session.writerQ); got != 1 {
		t.Fatalf("retained writer queue length = %d, want 1", got)
	}
	if request := <-session.writerQ; request.generation != 99 {
		t.Fatalf("retained writer generation = %d, want 99", request.generation)
	}
	select {
	case err := <-completion:
		var target *SessionError
		if !errors.As(err, &target) || target.Category != "stale_generation" {
			t.Fatalf("old user writer completion = %T %#v", err, err)
		}
	default:
		t.Fatal("old user writer completion was left pending")
	}
}

func TestReceiveOverflowBackpressuresUntilControlIsAccepted(t *testing.T) {
	session := &Session{
		receiveQ: make(chan receiveEvent, 1),
		controlQ: make(chan controlEvent),
	}
	session.generationState.Store(7)
	session.receiveQ <- receiveEvent{generation: 7, payload: []byte("queue-full")}
	returned := make(chan struct{})
	go func() {
		session.enqueueReceive(7, []byte("overflow"))
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("overflow callback returned before its control event was accepted")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case event := <-session.controlQ:
		if event.kind != controlReceiveOverflow || event.generation != 7 {
			t.Fatalf("overflow control = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow control event was not delivered")
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("overflow callback remained blocked after control acceptance")
	}
}
