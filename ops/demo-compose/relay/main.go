// Binary demo-relay forwards a fixed set of local experiment ports into the
// app containers' shared loopback. It does not accept a destination from clients.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const upstreamHost = "host.docker.internal"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ports []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if len(ports) == 0 {
		return errors.New("no demo ports supplied")
	}
	listeners := make([]net.Listener, 0, len(ports))
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for _, port := range ports {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return fmt.Errorf("invalid demo port %q", port)
		}
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			return err
		}
		listeners = append(listeners, listener)
		go forward(ctx, listener, net.JoinHostPort(upstreamHost, port))
	}
	if err := os.WriteFile("/tmp/demo-relay.ready", nil, 0600); err != nil {
		return err
	}
	defer os.Remove("/tmp/demo-relay.ready")
	<-ctx.Done()
	return nil
}

func forward(ctx context.Context, listener net.Listener, upstream string) {
	for {
		downstream, err := listener.Accept()
		if err != nil {
			return
		}
		go relay(ctx, downstream.(*net.TCPConn), upstream)
	}
}

func relay(ctx context.Context, downstream *net.TCPConn, upstream string) {
	defer downstream.Close()
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp4", upstream)
	if err != nil {
		return
	}
	target := conn.(*net.TCPConn)
	defer target.Close()
	stop := context.AfterFunc(ctx, func() {
		_ = target.Close()
		_ = downstream.Close()
	})
	defer stop()
	var drain sync.WaitGroup
	drain.Go(func() {
		_, _ = io.Copy(target, downstream)
		_ = target.CloseWrite()
	})
	_, _ = io.Copy(downstream, target)
	_ = downstream.CloseWrite()
	drain.Wait()
}
