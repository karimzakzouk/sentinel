package lead

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

// Of two replicas one works and the other stands by; when the first stops,
// the second takes over, and never while the first still works.
func TestOneReplicaWorksAtATime(t *testing.T) {
	client := fake.NewClientset()
	var working atomic.Int32
	var overlap atomic.Bool
	started := make(chan string, 2)
	replica := func(ctx context.Context, name string) error {
		c := Config{Namespace: "sentinel", Name: "sentinel-fixer", Identity: name,
			LeaseDuration: 2 * time.Second, RenewDeadline: 1500 * time.Millisecond, RetryPeriod: 200 * time.Millisecond}
		return Run(ctx, client, c, slog.New(slog.DiscardHandler), func(ctx context.Context) error {
			if working.Add(1) > 1 {
				overlap.Store(true)
			}
			defer working.Add(-1)
			started <- name
			<-ctx.Done()
			return nil
		})
	}
	firstCtx, stopFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- replica(firstCtx, "a") }()
	if got := <-started; got != "a" {
		t.Fatalf("%s worked first", got)
	}
	secondCtx, stopSecond := context.WithCancel(context.Background())
	defer stopSecond()
	secondDone := make(chan error, 1)
	go func() { secondDone <- replica(secondCtx, "b") }()
	// Longer than a lease: a renews it, so b must keep standing by.
	select {
	case got := <-started:
		t.Fatalf("%s worked while a held the lease", got)
	case <-time.After(3 * time.Second):
	}
	stopFirst()
	if err := <-firstDone; err != nil {
		t.Fatalf("a stopped with %v", err)
	}
	select {
	case got := <-started:
		if got != "b" {
			t.Fatalf("%s took over", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("b never took over")
	}
	stopSecond()
	if err := <-secondDone; err != nil || overlap.Load() {
		t.Fatalf("b stopped with %v; overlap %v", err, overlap.Load())
	}
}

// Work that fails ends the run with its error.
func TestWorkThatFailsEndsTheRun(t *testing.T) {
	boom := errors.New("boom")
	c := Config{Namespace: "sentinel", Name: "sentinel-lab", Identity: "a",
		LeaseDuration: 2 * time.Second, RenewDeadline: 1500 * time.Millisecond, RetryPeriod: 200 * time.Millisecond}
	err := Run(context.Background(), fake.NewClientset(), c, slog.New(slog.DiscardHandler), func(context.Context) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
