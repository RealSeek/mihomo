package dialer

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestParallelDialCancelsLosingAttempt(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	winner, peer := net.Pipe()
	defer peer.Close()
	defer winner.Close()
	dial := NetDialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "192.0.2.1:443" {
			select {
			case <-started:
				return winner, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := parallelDialContext(ctx, "tcp4", []netip.Addr{
		netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2"),
	}, "443", option{netDialer: dial})
	if err != nil || conn != winner {
		t.Fatalf("winning connection = %v, error = %v", conn, err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("losing dial was not canceled after a connection succeeded")
	}
	if ctx.Err() != nil {
		t.Fatal("parallel dialing canceled its caller's context")
	}
}
