package download

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func noDelays(t *testing.T) {
	t.Helper()
	saved := Delays
	Delays = make([]time.Duration, len(saved))
	t.Cleanup(func() { Delays = saved })
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRetryRecoversFromTransientFailures(t *testing.T) {
	noDelays(t)
	var logs bytes.Buffer
	calls := 0
	err := Retry(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), "https://example.com/x", func() error {
		calls++
		if calls < 3 {
			return errors.New("504 Gateway Time-out")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if !strings.Contains(logs.String(), "attempt=2") || !strings.Contains(logs.String(), "504 Gateway Time-out") {
		t.Errorf("retries must be logged with the cause: %s", logs.String())
	}
}

func TestRetryReturnsLastErrorWhenSpent(t *testing.T) {
	noDelays(t)
	calls := 0
	err := Retry(context.Background(), discard(), "https://example.com/x", func() error {
		calls++
		return errors.New("attempt failed")
	})
	if err == nil || err.Error() != "attempt failed" {
		t.Fatalf("err = %v, want the last attempt's error", err)
	}
	if calls != len(Delays)+1 {
		t.Errorf("calls = %d, want %d", calls, len(Delays)+1)
	}
}

func TestRetryStopsWhenContextEnds(t *testing.T) {
	saved := Delays
	Delays = []time.Duration{time.Hour}
	t.Cleanup(func() { Delays = saved })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	calls := 0
	err := Retry(ctx, discard(), "https://example.com/x", func() error {
		calls++
		return errors.New("attempt failed")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context error", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want no attempt after the context ended", calls)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("waited %s after the context ended", elapsed)
	}
}

// statusThenBody fails the first n requests with status, then serves body.
func statusThenBody(n int32, status int, body string) (*httptest.Server, *int32) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) <= n {
			w.WriteHeader(status)
			io.WriteString(w, "<html>error page</html>")
			return
		}
		io.WriteString(w, body)
	}))
	return srv, &requests
}

func TestURLRetriesServerErrors(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	noDelays(t)
	srv, requests := statusThenBody(2, http.StatusGatewayTimeout, "kind: Namespace\n")
	defer srv.Close()

	data, err := URL(context.Background(), discard(), srv.URL+"/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "kind: Namespace\n" {
		t.Errorf("data = %q, want the manifest", data)
	}
	if *requests != 3 {
		t.Errorf("requests = %d, want 3", *requests)
	}
}

func TestURLNeverReturnsErrorPages(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	noDelays(t)
	srv, requests := statusThenBody(100, http.StatusTooManyRequests, "")
	defer srv.Close()

	data, err := URL(context.Background(), discard(), srv.URL+"/manifest.yaml")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want the HTTP status", err)
	}
	if data != nil {
		t.Errorf("returned an error page as content: %q", data)
	}
	if *requests != int32(len(Delays)+1) {
		t.Errorf("requests = %d, want %d", *requests, len(Delays)+1)
	}
}
