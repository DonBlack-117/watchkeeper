// Package notify delivers alerts. Messages go through a small queue so a slow
// or failing channel never delays the checks.
package notify

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Message is one alert. Text is Telegram HTML: callers escape dynamic values.
type Message struct {
	Text string
}

// Notifier sends one message to one channel.
type Notifier interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// RetryAfter is returned by a Notifier that was rate limited and knows when to retry.
type RetryAfter struct {
	Wait time.Duration
	Err  error
}

func (e *RetryAfter) Error() string { return e.Err.Error() }
func (e *RetryAfter) Unwrap() error { return e.Err }

// Permanent marks an error that retrying cannot fix, such as a bad token.
type Permanent struct{ Err error }

func (e *Permanent) Error() string { return e.Err.Error() }
func (e *Permanent) Unwrap() error { return e.Err }

// Recorder counts deliveries; the metrics package implements it.
type Recorder interface {
	Notification(channel, result string)
}

const (
	queueSize   = 100
	maxAttempts = 4
	sendTimeout = 10 * time.Second
	maxWait     = time.Minute
)

// Dispatcher fans each message out to every notifier from one background worker.
type Dispatcher struct {
	notifiers []Notifier
	log       *slog.Logger
	rec       Recorder
	queue     chan Message
	backoff   func(attempt int) time.Duration
	done      chan struct{}
}

// NewDispatcher returns a dispatcher; with no notifiers, Enqueue drops messages.
func NewDispatcher(log *slog.Logger, rec Recorder, notifiers ...Notifier) *Dispatcher {
	return &Dispatcher{
		notifiers: notifiers,
		log:       log,
		rec:       rec,
		queue:     make(chan Message, queueSize),
		backoff:   func(attempt int) time.Duration { return time.Duration(1<<attempt) * time.Second },
		done:      make(chan struct{}),
	}
}

// Enabled reports whether any channel is configured.
func (d *Dispatcher) Enabled() bool { return len(d.notifiers) > 0 }

// Channels lists the configured channel names.
func (d *Dispatcher) Channels() []string {
	out := make([]string, 0, len(d.notifiers))
	for _, n := range d.notifiers {
		out = append(out, n.Name())
	}
	return out
}

// Enqueue adds a message without blocking. It returns false when there is no
// channel or the queue is full; the message is then dropped and logged.
func (d *Dispatcher) Enqueue(m Message) bool {
	if !d.Enabled() {
		return false
	}
	select {
	case d.queue <- m:
		return true
	default:
		d.log.Error("alert queue full; message dropped", "queued", len(d.queue))
		d.record("all", "dropped")
		return false
	}
}

// SendNow delivers m to every channel right away, without retries. The test
// endpoint uses it so the person who pressed the button sees the real error.
func (d *Dispatcher) SendNow(ctx context.Context, m Message) error {
	var errs []error
	for _, n := range d.notifiers {
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		err := n.Send(sctx, m)
		cancel()
		d.record(n.Name(), result(err))
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run delivers queued messages until stop is cancelled, then tries for up to
// `drain` to send what is still queued. stop is only a signal: deliveries use
// their own timeouts, so a message being sent when it fires is not cut short.
// main cancels stop after the checks have finished, so their last alerts are
// already queued.
func (d *Dispatcher) Run(stop context.Context, drain time.Duration) {
	defer close(d.done)
	ctx := context.WithoutCancel(stop)
	for {
		select {
		case m := <-d.queue:
			d.deliver(ctx, m)
		case <-stop.Done():
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
			defer cancel()
			for {
				select {
				case m := <-d.queue:
					d.deliver(dctx, m)
				default:
					return
				}
				if dctx.Err() != nil {
					d.log.Warn("alerts left unsent at shutdown", "queued", len(d.queue))
					return
				}
			}
		}
	}
}

// Done is closed when Run has returned.
func (d *Dispatcher) Done() <-chan struct{} { return d.done }

func (d *Dispatcher) deliver(ctx context.Context, m Message) {
	for _, n := range d.notifiers {
		var err error
		for attempt := range maxAttempts {
			sctx, cancel := context.WithTimeout(ctx, sendTimeout)
			err = n.Send(sctx, m)
			cancel()
			var perm *Permanent
			if err == nil || errors.As(err, &perm) || attempt == maxAttempts-1 {
				break
			}
			wait := d.backoff(attempt)
			var ra *RetryAfter
			if errors.As(err, &ra) && ra.Wait > 0 {
				wait = min(ra.Wait, maxWait)
			}
			d.log.Warn("alert failed; retrying", "channel", n.Name(), "attempt", attempt+1, "wait", wait, "err", err)
			if !sleep(ctx, wait) {
				break
			}
		}
		d.record(n.Name(), result(err))
		if err != nil {
			d.log.Error("alert not delivered", "channel", n.Name(), "err", err)
		}
	}
}

func (d *Dispatcher) record(channel, res string) {
	if d.rec != nil {
		d.rec.Notification(channel, res)
	}
}

func result(err error) string {
	if err != nil {
		return "error"
	}
	return "sent"
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
