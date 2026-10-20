package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const token = "123456789:AAH-abcdefghijklmnopqrstuvwxyz0123456"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func telegramAt(url string) *Telegram {
	t := NewTelegram(token, "-1001234")
	t.base = url
	return t
}

func TestTelegramSend(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/sendMessage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	if err := telegramAt(srv.URL).Send(context.Background(), Message{Text: "<b>hi</b>"}); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "-1001234" || got["parse_mode"] != "HTML" || got["text"] != "<b>hi</b>" {
		t.Fatalf("body: %v", got)
	}
}

func TestTelegramErrors(t *testing.T) {
	cases := []struct {
		code int
		body string
		perm bool
		wait time.Duration
	}{
		{429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`, false, 7 * time.Second},
		{401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, true, 0},
		{400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, true, 0},
		{502, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`, false, 0},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.code)
			_, _ = io.WriteString(w, c.body)
		}))
		err := telegramAt(srv.URL).Send(context.Background(), Message{Text: "x"})
		srv.Close()
		var perm *Permanent
		var ra *RetryAfter
		if err == nil || errors.As(err, &perm) != c.perm {
			t.Errorf("%d: err = %v, permanent = %v", c.code, err, c.perm)
		}
		if c.wait > 0 && (!errors.As(err, &ra) || ra.Wait != c.wait) {
			t.Errorf("%d: retry after = %v", c.code, err)
		}
	}
}

func TestTelegramErrorsNeverContainTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	err := telegramAt(url).Send(context.Background(), Message{Text: "x"})
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "AAH-") {
		t.Fatalf("error leaks the token or is nil: %v", err)
	}
}

type fakeNotifier struct {
	mu    sync.Mutex
	fails int // fail this many times before succeeding
	err   error
	got   []string
	calls atomic.Int32
}

func (f *fakeNotifier) Name() string { return "fake" }

func (f *fakeNotifier) Send(_ context.Context, m Message) error {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return f.err
	}
	f.got = append(f.got, m.Text)
	return nil
}

type counts struct {
	mu sync.Mutex
	m  map[string]int
}

func (c *counts) Notification(channel, result string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[channel+"/"+result]++
}

func (c *counts) get(k string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[k]
}

func fastDispatcher(rec Recorder, n ...Notifier) *Dispatcher {
	d := NewDispatcher(quiet(), rec, n...)
	d.backoff = func(int) time.Duration { return time.Millisecond }
	return d
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not met")
}

func TestDispatcherRetriesThenDelivers(t *testing.T) {
	n := &fakeNotifier{fails: 2, err: errors.New("temporary")}
	rec := &counts{m: map[string]int{}}
	d := fastDispatcher(rec, n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx, time.Second)
	d.Enqueue(Message{Text: "a"})
	eventually(t, func() bool { return rec.get("fake/sent") == 1 })
	if n.calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", n.calls.Load())
	}
}

func TestDispatcherStopsOnPermanentError(t *testing.T) {
	n := &fakeNotifier{fails: 10, err: &Permanent{errors.New("bad token")}}
	rec := &counts{m: map[string]int{}}
	d := fastDispatcher(rec, n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx, time.Second)
	d.Enqueue(Message{Text: "a"})
	eventually(t, func() bool { return rec.get("fake/error") == 1 })
	if n.calls.Load() != 1 {
		t.Fatalf("a permanent error must not be retried, calls = %d", n.calls.Load())
	}
}

func TestDispatcherDrainsOnShutdownAndDropsWhenFull(t *testing.T) {
	n := &fakeNotifier{}
	rec := &counts{m: map[string]int{}}
	d := fastDispatcher(rec, n)
	for range queueSize {
		if !d.Enqueue(Message{Text: "x"}) {
			t.Fatal("queue rejected a message before it was full")
		}
	}
	if d.Enqueue(Message{Text: "overflow"}) || rec.get("all/dropped") != 1 {
		t.Fatal("a full queue must drop and count the message")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already shutting down: Run only drains
	d.Run(ctx, time.Second)
	if rec.get("fake/sent") != queueSize {
		t.Fatalf("sent %d of %d queued messages", rec.get("fake/sent"), queueSize)
	}
	<-d.Done()
}

func TestNoNotifiers(t *testing.T) {
	d := NewDispatcher(quiet(), nil)
	if d.Enabled() || d.Enqueue(Message{Text: "x"}) || len(d.Channels()) != 0 {
		t.Fatal("a dispatcher without channels must be disabled")
	}
}

type slowNotifier struct {
	fakeNotifier
	started chan struct{}
	release chan struct{}
}

func (s *slowNotifier) Send(ctx context.Context, m Message) error {
	close(s.started)
	<-s.release
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.fakeNotifier.Send(ctx, m)
}

func TestStopDoesNotAbortADeliveryInProgress(t *testing.T) {
	n := &slowNotifier{started: make(chan struct{}), release: make(chan struct{})}
	rec := &counts{m: map[string]int{}}
	d := fastDispatcher(rec, n)
	stop, cancel := context.WithCancel(context.Background())
	go d.Run(stop, time.Second)
	d.Enqueue(Message{Text: "last words"})
	<-n.started
	cancel() // SIGTERM while the message is being sent
	close(n.release)
	<-d.Done()
	if rec.get("fake/sent") != 1 {
		t.Fatalf("the message in flight must still be delivered: %v", rec.m)
	}
}

func TestLongMessageFallsBackToPlainText(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	text := "<b>" + strings.Repeat("&amp;", 2000) + "</b>"
	if err := telegramAt(srv.URL).Send(context.Background(), Message{Text: text}); err != nil {
		t.Fatal(err)
	}
	if _, html := got["parse_mode"]; html || len([]rune(got["text"].(string))) > 4096 || strings.Contains(got["text"].(string), "<b>") {
		t.Fatalf("long message: parse_mode=%v len=%d", got["parse_mode"], len([]rune(got["text"].(string))))
	}
}
