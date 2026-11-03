package net_test

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	N "github.com/metacubex/mihomo/common/net"
)

func TestRelayHalfCloseAndReset(t *testing.T) {
	pair := func(t *testing.T) (*net.TCPConn, *net.TCPConn) {
		t.Helper()
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		client, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		server, err := listener.AcceptTCP()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Close() })
		deadline := time.Now().Add(5 * time.Second)
		_ = client.SetDeadline(deadline)
		_ = server.SetDeadline(deadline)
		return client, server
	}

	t.Run("half-close keeps reply", func(t *testing.T) {
		client, left := pair(t)
		right, server := pair(t)
		done := make(chan N.RelayResult, 1)
		go func() { done <- N.Relay(left, right) }()
		if _, err := client.Write([]byte("request")); err != nil {
			t.Fatal(err)
		}
		if err := client.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		request, err := io.ReadAll(server)
		if err != nil || string(request) != "request" {
			t.Fatalf("server request = %q, %v", request, err)
		}
		if _, err := server.Write([]byte("reply")); err != nil {
			t.Fatal(err)
		}
		if err := server.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		reply, err := io.ReadAll(client)
		if err != nil || string(reply) != "reply" {
			t.Fatalf("client reply = %q, %v", reply, err)
		}
		result := <-done
		if result.First != N.RelayLeftToRight || result.LeftToRight.Bytes != 7 || result.RightToLeft.Bytes != 5 || result.LeftToRight.Err != nil || result.RightToLeft.Err != nil {
			t.Fatalf("relay result = %+v", result)
		}
	})

	t.Run("remote reset precedes induced close", func(t *testing.T) {
		_, left := pair(t)
		right, server := pair(t)
		done := make(chan N.RelayResult, 1)
		go func() { done <- N.Relay(left, right) }()
		if err := server.SetLinger(0); err != nil {
			t.Fatal(err)
		}
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		result := <-done
		if result.First != N.RelayRightToLeft || result.RightToLeft.Err == nil || errors.Is(result.RightToLeft.Err, net.ErrClosed) {
			t.Fatalf("remote reset lost or misattributed: %+v", result)
		}
		if !errors.Is(result.LeftToRight.Err, net.ErrClosed) {
			t.Fatalf("expected induced local close, got %+v", result)
		}
	})
}
