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

func TestAuthFrameRejectsMiBWithoutAlloc(t *testing.T) {
	var buf bytes.Buffer
	// Declare 1 MiB — above MaxAuthFrameBytes, well below MaxFrameBytes.
	_ = binaryBigEndianPutUint32(&buf, 1<<20)
	_, err := readFrameLimit(&buf, MaxAuthFrameBytes)
	if err == nil {
		t.Fatal("expected rejection of 1 MiB pre-auth frame")
	}
}

func binaryBigEndianPutUint32(buf *bytes.Buffer, n uint32) error {
	var hdr [4]byte
	hdr[0] = byte(n >> 24)
	hdr[1] = byte(n >> 16)
	hdr[2] = byte(n >> 8)
	hdr[3] = byte(n)
	_, err := buf.Write(hdr[:])
	return err
}
