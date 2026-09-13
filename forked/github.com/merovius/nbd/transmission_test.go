package nbd

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListenAndServeContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()

	sockFile := filepath.Join(dir, "nbd.sock")

	// Start the server
	exited := make(chan any)
	go func() {
		defer close(exited)
		err := ListenAndServe(ctx, "unix", sockFile, Export{})
		if err != nil {
			t.Errorf("ListenAndServe returned an error: %v", err)
		}
	}()

	// Simulate the server working for some time
	time.Sleep(100 * time.Millisecond)

	// Test cancelling the context
	cancel()

	select {
	case <-time.After(1 * time.Second):
		t.Error("Server did not shut down after context was cancelled")
	case <-exited:
		// The server context should be cancelled, and the server should shut down, within a very short time.
	}
}

func TestListenAndServeContextNoCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()

	sockFile := filepath.Join(dir, "nbd.sock")

	// Start the server
	exited := make(chan any)
	go func() {
		defer close(exited)
		err := ListenAndServe(ctx, "unix", sockFile, Export{})
		if err != nil {
			t.Errorf("ListenAndServe returned an error: %v", err)
		}
	}()

	// Simulate the server working for some time
	time.Sleep(100 * time.Millisecond)

	select {
	case <-time.After(100 * time.Millisecond):
		// No cancel was called, so we are stuck in ListenAndServe
	case <-exited:
		t.Error("Server did not shut down after context was cancelled")
	}
}
