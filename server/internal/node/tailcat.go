package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/tailscale/tailcat"

	"github.com/nwverse/persona/server/internal/tunnel"
)

// ServeTailcat brings up this machine's tailcat endpoint for a resident node, when it was set up for it
// (`personad node tailcat init`, ~/.persona/tailcat.json): the hub's connections on tunnel.NodePort go to the returned
// channel (Attach's extra), a new binary on tunnel.UploadPort replaces this one and calls upgraded. Not set up: a nil
// channel. Starting needs the network (the DERP map, the relay), so it retries in the background until it works:
// relays over ssh are served meanwhile. stopped closes once ctx ended and the endpoint is down.
func ServeTailcat(ctx context.Context, logger *log.Logger, upgraded func()) (conns <-chan net.Conn, stopped <-chan struct{}, err error) {
	done := make(chan struct{})
	k, err := tunnel.LoadNodeKey(tunnel.NodeKeyPath())
	if err == nil {
		var exe string
		if exe, err = tunnel.Executable(); err == nil {
			c := make(chan net.Conn)
			ctx, cancel := context.WithCancel(ctx)
			tailcatStop.Store(&tailcatEndpoint{cancel: cancel, logger: logger})
			go retryTailcat(ctx, k, logger, exe, c, upgraded, done)
			return c, done, nil
		}
	}
	close(done)
	if errors.Is(err, os.ErrNotExist) {
		return nil, done, nil
	}
	return nil, done, err
}

// tailcatEndpoint is the endpoint ServeTailcat started in this process, for stopTailcat.
type tailcatEndpoint struct {
	cancel context.CancelFunc
	logger *log.Logger
}

// tailcatStop is this process's tailcat endpoint; nil when it serves none.
var tailcatStop atomic.Pointer[tailcatEndpoint]

// stopTailcat ends this process's tailcat endpoint for good, its relay connections and DERP map fetches included:
// the hub is in office network mode (node.configure private_network). The endpoint stays down until the node restarts
// and the hub says otherwise; a node that serves none does nothing.
func stopTailcat() {
	if e := tailcatStop.Swap(nil); e != nil {
		e.logger.Printf("tailcat: stopped, the hub is in office network mode (delete ~/.persona/tailcat.json to keep it from starting)")
		e.cancel()
	}
}

func retryTailcat(ctx context.Context, k *tunnel.NodeKey, logger *log.Logger, exe string, conns chan<- net.Conn, upgraded func(), done chan<- struct{}) {
	defer close(done)
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := serveTailcatOnce(ctx, k, logger, exe, conns, upgraded)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 10*time.Minute { // it was working: a new failure starts the backoff over
			backoff = 5 * time.Second
		}
		logger.Printf("tailcat: %v; again in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Minute)
	}
}

// serveTailcatOnce runs one tailcat server until ctx ends or it fails to start.
func serveTailcatOnce(ctx context.Context, k *tunnel.NodeKey, logger *log.Logger, exe string, conns chan<- net.Conn, upgraded func()) error {
	s := k.Server(tunnel.Logf(logger.Printf))
	defer s.Close()
	sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	ln, err := s.Listen(sctx, "tcp", fmt.Sprintf(":%d", tunnel.NodePort))
	cancel()
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	// Only a started server has a netstack to drain: DrainTCP on one that failed to start (no network, no DERP)
	// panics, which used to crash the resident node before retryTailcat could back off.
	defer func() { // let the closes of the connections handed out leave before the endpoint goes
		dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = s.DrainTCP(dctx)
		cancel()
	}()
	up, err := s.Listen(ctx, "tcp", fmt.Sprintf(":%d", tunnel.UploadPort))
	if err != nil {
		return fmt.Errorf("listen for uploads: %w", err)
	}
	// Never log the address: it is the secret that lets a client in (with the hub's key).
	logger.Printf("tailcat: serving %d hub key(s), DERP region %d", len(k.Hubs), k.RegionID)
	go tunnel.ServeUpload(up, exe, upgraded)
	stop := context.AfterFunc(ctx, func() { ln.Close(); up.Close() })
	defer stop()
	defer up.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		select {
		case conns <- c:
		case <-ctx.Done():
			c.Close()
			return nil
		}
	}
}

// TailcatAddr is this machine's address for hub.json (machines[].tailcat_addr).
func TailcatAddr() (tailcat.Addr, error) {
	k, err := tunnel.LoadNodeKey(tunnel.NodeKeyPath())
	if err != nil {
		return "", err
	}
	return k.Addr(), nil
}
