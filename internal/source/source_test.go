package source

import (
        "context"
	"time"
        "testing"
)

// fakeSource is a test Source implementation.
type fakeSource struct {
        name    string
        started bool
        stopped bool
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Start(ctx context.Context, h Handler) error {
        f.started = true
        <-ctx.Done()
        return nil
}
func (f *fakeSource) Stop() error {
        f.stopped = true
        return nil
}

// fakeHandler is a test Handler implementation.
type fakeHandler struct {
        signals []Signal
}

func (f *fakeHandler) Handle(s Signal) {
        f.signals = append(f.signals, s)
}

func TestFakeSourceLifecycle(t *testing.T) {
        s := &fakeSource{name: "test"}
        if s.Name() != "test" {
                t.Errorf("expected name 'test', got %q", s.Name())
        }

        ctx, cancel := context.WithCancel(context.Background())
        h := &fakeHandler{}

        // Start blocks until ctx is cancelled
        go func() {
                if err := s.Start(ctx, h); err != nil {
                        t.Errorf("Start returned error: %v", err)
                }
        }()

        // Give the goroutine time to start
        time.Sleep(10 * time.Millisecond)
        cancel()
        time.Sleep(10 * time.Millisecond)

        if err := s.Stop(); err != nil {
                t.Errorf("Stop returned error: %v", err)
        }

        if !s.started {
                t.Error("source was not started")
        }
        if !s.stopped {
                t.Error("source was not stopped")
        }
}

func TestSignalTypes(t *testing.T) {
        types := []SignalType{
                SignalLogError,
                SignalPodStatus,
                SignalK8sEvent,
                SignalAlertmanager,
                SignalDatadog,
                SignalManual,
        }

        for _, st := range types {
                if string(st) == "" {
                        t.Errorf("signal type %v has empty string representation", st)
                }
        }
}

func TestHandlerReceivesSignal(t *testing.T) {
        h := &fakeHandler{}
        sig := Signal{
                Type:      SignalLogError,
                Service:   "test-svc",
                Namespace: "default",
                Reason:    "TypeError in main.py",
                Repo:      "org/repo",
                RawLogs:   "Traceback...",
                Labels:    map[string]string{"severity": "critical"},
        }

        h.Handle(sig)

        if len(h.signals) != 1 {
                t.Fatalf("expected 1 signal, got %d", len(h.signals))
        }

        got := h.signals[0]
        if got.Service != "test-svc" {
                t.Errorf("service: got %q, want test-svc", got.Service)
        }
        if got.Reason != "TypeError in main.py" {
                t.Errorf("reason: got %q", got.Reason)
        }
}
