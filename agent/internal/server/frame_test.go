package server

import (
	"bytes"
	"testing"

	"github.com/spadeide/spade-agent/internal/protocol"
)

func TestFrameRoundTrip(t *testing.T) {
	env := protocol.NewResponse("abc", map[string]string{"k": "v"})
	var buf bytes.Buffer
	if err := writeFrame(&buf, env); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "abc" || got.OK == nil || !*got.OK {
		t.Fatalf("got %+v", got)
	}
}

func TestFrameRejectsHuge(t *testing.T) {
	var buf bytes.Buffer
	// Hand-craft an oversize length header.
	buf.Write([]byte{0xff, 0xff, 0xff, 0xff})
	_, err := readFrame(&buf)
	if err == nil {
		t.Fatal("expected error")
	}
}
