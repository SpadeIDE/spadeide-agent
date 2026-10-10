package server

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/spadeide/spade-agent/internal/protocol"
)

func TestStreamMuxRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	client := newStreamMux(a)
	server := newStreamMux(b)
	go client.Serve()
	go server.Serve()
	defer client.Close()
	defer server.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		env, err := readFrame(server.Stream(StreamRPC))
		if err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if env.Method != "fs.read" {
			t.Errorf("method=%q", env.Method)
		}
		_ = writeFrame(server.Stream(StreamPTY), protocol.Envelope{
			Event:  "pty.data",
			Result: []byte(`{"x":1}`),
		})
	}()

	if err := writeFrame(client.Stream(StreamRPC), protocol.Envelope{
		ID:     "1",
		Method: "fs.read",
		Params: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	_ = a.SetDeadline(time.Now().Add(2 * time.Second))
	env, err := readFrame(client.Stream(StreamPTY))
	if err != nil {
		t.Fatal(err)
	}
	if env.Event != "pty.data" {
		t.Fatalf("event=%q", env.Event)
	}
	wg.Wait()
}

func TestStreamMuxConcurrentStreams(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	client := newStreamMux(a)
	server := newStreamMux(b)
	go client.Serve()
	go server.Serve()
	defer client.Close()
	defer server.Close()

	got := make(chan string, 1)
	go func() {
		env, err := readFrame(server.Stream(StreamPTY))
		if err != nil {
			t.Errorf("pty read: %v", err)
			return
		}
		got <- env.Method
	}()

	// Large control write in flight should not block pty delivery on the peer
	// demux (same TCP pipe, but pty frame is discrete).
	go func() {
		_ = writeFrame(client.Stream(StreamControl), protocol.Envelope{
			ID:     "big",
			Method: "hello",
			Params: []byte(`{}`),
		})
	}()

	if err := writeFrame(client.Stream(StreamPTY), protocol.Envelope{
		ID:     "p",
		Method: "ping",
		Params: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case m := <-got:
		if m != "ping" {
			t.Fatalf("got %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for pty frame")
	}
}

func TestStreamMuxRejectsPreAuthHugeFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	server := newStreamMux(b)
	done := make(chan struct{})
	go func() {
		server.Serve()
		close(done)
	}()
	defer server.Close()

	// 1 MiB declared — above MaxAuthFrameBytes, no UnlockFrames yet.
	var hdr [5]byte
	hdr[0] = StreamRPC
	binary.BigEndian.PutUint32(hdr[1:5], 1<<20)
	if _, err := a.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not exit on oversize pre-auth frame")
	}
	if server.readErr == nil {
		t.Fatal("expected readErr")
	}
}

func TestStreamMuxBufferCapBeforeUnlock(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	server := newStreamMux(b)
	done := make(chan struct{})
	go func() {
		server.Serve()
		close(done)
	}()
	defer server.Close()

	// Two MaxAuthFrameBytes frames into rpc without a reader → buffer overflow.
	payload := make([]byte, MaxAuthFrameBytes)
	for i := 0; i < 2; i++ {
		var hdr [5]byte
		hdr[0] = StreamRPC
		binary.BigEndian.PutUint32(hdr[1:5], uint32(len(payload)))
		if _, err := a.Write(hdr[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Write(payload); err != nil && err != io.ErrClosedPipe {
			// second write may fail once Serve closed
			if i == 0 {
				t.Fatal(err)
			}
		}
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not exit on buffer overflow")
	}
}
