package legacyvmctl

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func waitSeed[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for seed server")
		var zero T
		return zero
	}
}

func TestSeedServerCancelsRequestsAndWaitsForHandlers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- serveSeedHTTP(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			<-finish
			w.WriteHeader(http.StatusNoContent)
		}))
	}()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()
	waitSeed(t, entered)
	cancel()
	waitSeed(t, canceled)
	select {
	case err := <-done:
		t.Fatalf("seed server returned with a handler still running: %v", err)
	default:
	}
	close(finish)
	if err := waitSeed(t, done); err != nil {
		t.Fatal(err)
	}
	waitSeed(t, clientDone)
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("seed listener remains open after shutdown")
	}
}

func TestSeedsCancelWhileAddressUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Controller{}
	if err := c.serveSeeds(ctx, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
}
