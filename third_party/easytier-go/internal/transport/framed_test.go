package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestStreamTunnelFraming(t *testing.T) {
	left, right := net.Pipe()
	tunnel := NewStreamTunnel(left)
	body := make([]byte, peerManagerHeaderSize+7)
	copy(body[peerManagerHeaderSize:], "payload")
	sendDone := make(chan error, 1)
	go func() {
		var header [4]byte
		binary.LittleEndian.PutUint32(header[:], uint32(len(body)))
		if _, err := right.Write(header[:]); err != nil {
			sendDone <- err
			return
		}
		_, err := right.Write(body)
		sendDone <- err
	}()
	got, err := tunnel.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got[peerManagerHeaderSize:]) != "payload" || len(got) != len(body) {
		t.Fatalf("received body = %x", got)
	}
	if err := <-sendDone; err != nil {
		t.Fatal(err)
	}
	_ = tunnel.Close()
}

func TestStreamTunnelSendAndValidation(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	tunnel := NewStreamTunnel(left)
	body := make([]byte, peerManagerHeaderSize+3)
	readDone := make(chan []byte, 1)
	go func() {
		encoded := make([]byte, 4+len(body))
		_, _ = io.ReadFull(right, encoded)
		readDone <- encoded
	}()
	if err := tunnel.Send(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	encoded := <-readDone
	if got := binary.LittleEndian.Uint32(encoded[:4]); got != uint32(len(body)) || string(encoded[4+peerManagerHeaderSize:]) != string(body[peerManagerHeaderSize:]) {
		t.Fatalf("encoded body = %x", encoded)
	}
	if err := tunnel.Send(context.Background(), make([]byte, peerManagerHeaderSize-1)); err == nil {
		t.Fatal("short body accepted")
	}
	if err := tunnel.Send(context.Background(), make([]byte, tcpTunnelMaxBody+1)); err == nil {
		t.Fatal("oversized body accepted")
	}
	_ = tunnel.Close()
}

func TestStreamTunnelContextCancellation(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	tunnel := NewStreamTunnel(left)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := tunnel.Receive(ctx)
		result <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("receive cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("receive did not observe cancellation")
	}
	_ = tunnel.Close()
}
