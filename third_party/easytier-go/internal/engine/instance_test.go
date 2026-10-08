package engine

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/easytier/easytier/easytier-go/internal/coreabi"
	"github.com/easytier/easytier/easytier-go/platform/netstd"
)

const minimalConfig = `instance_id = "018f4fb1-7a2c-7d1f-9d89-935b0ad7e135"
instance_name = "go-host-engine"

[network_identity]
network_name = "default"
network_secret = "test"

[flags]
disable_p2p = true
enable_encryption = false
bind_device = false
`

func TestHostCreatesAndDrivesInstanceLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, err := NewHost(ctx, Options{Services: netstd.Services()})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer host.Close(ctx)
	instance, err := host.CreateInstance(ctx, minimalConfig)
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	defer instance.Close(ctx)

	if err := instance.Start(ctx); err != nil {
		t.Fatalf("start instance: %v", err)
	}
	if state := instance.State(); state != 2 {
		t.Fatalf("running state = %d, want 2", state)
	}
	if err := instance.Stop(ctx); err != nil {
		t.Fatalf("stop instance: %v", err)
	}
	if err := instance.Wait(ctx); err != nil {
		t.Fatalf("wait for stopped instance: %v", err)
	}
}

func TestClosingOneHandleLeavesOtherInstanceRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, err := NewHost(ctx, Options{Services: netstd.Services()})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer host.Close(ctx)

	first, err := host.CreateInstance(ctx, minimalConfig)
	if err != nil {
		t.Fatalf("create first instance: %v", err)
	}
	secondConfig := strings.NewReplacer(
		"018f4fb1-7a2c-7d1f-9d89-935b0ad7e135",
		"018f4fb1-7a2c-7d1f-9d89-935b0ad7e136",
		"go-host-engine",
		"go-host-engine-second",
	).Replace(minimalConfig)
	second, err := host.CreateInstance(ctx, secondConfig)
	if err != nil {
		t.Fatalf("create second instance: %v", err)
	}
	defer second.Close(ctx)
	if err := first.Start(ctx); err != nil {
		t.Fatalf("start first instance: %v", err)
	}
	if err := second.Start(ctx); err != nil {
		t.Fatalf("start second instance: %v", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatalf("close first instance: %v", err)
	}
	if state := first.State(); state != coreabi.StateStopped {
		t.Fatalf("closed first state = %d, want stopped", state)
	}
	if state := second.State(); state != 2 {
		t.Fatalf("second state after first close = %d, want running", state)
	}
	if err := second.Stop(ctx); err != nil {
		t.Fatalf("stop second instance: %v", err)
	}
}

func TestClosingInstanceStopsAcceptingRPCBeforeDriverShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	host, err := NewHost(ctx, Options{Services: netstd.Services()})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	instance, err := host.CreateInstance(ctx, minimalConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(ctx); err != nil {
		t.Fatal(err)
	}
	func() {
		// Hold the guest while the driver accepts a command, reproducing the
		// interval between a close request and the driver's final state update.
		host.guestMu.Lock()
		defer host.guestMu.Unlock()
		select {
		case instance.commands <- command{kind: commandStop, response: make(chan error, 1)}:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		closedContext, cancelClose := context.WithCancel(ctx)
		cancelClose()
		if err := instance.Close(closedContext); !errors.Is(err, context.Canceled) {
			t.Fatalf("close before shutdown = %v", err)
		}
		if state := instance.State(); state != coreabi.StateStopping {
			t.Fatalf("closing state = %v, want stopping", state)
		}
		if _, err := instance.RPC(ctx, nil); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("RPC on closing instance = %v", err)
		}
	}()
	if err := instance.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if state := instance.State(); state != coreabi.StateStopped {
		t.Fatalf("closed state = %v, want stopped", state)
	}
}
