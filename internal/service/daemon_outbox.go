package service

import (
	"context"
	"sync"
)

type daemonOutboxContextKey struct{}

// DaemonOutbox tracks only work that an agent or human already authorized.
// It is not a scheduler: callers must first commit the normal prepared and
// dispatched saga records before handing the external adapter call to it.
type DaemonOutbox struct {
	wait sync.WaitGroup
}

func NewDaemonOutbox() *DaemonOutbox { return &DaemonOutbox{} }

func WithDaemonOutbox(ctx context.Context, outbox *DaemonOutbox) context.Context {
	if outbox == nil {
		return ctx
	}
	return context.WithValue(ctx, daemonOutboxContextKey{}, outbox)
}

func daemonOutboxFromContext(ctx context.Context) *DaemonOutbox {
	value, _ := ctx.Value(daemonOutboxContextKey{}).(*DaemonOutbox)
	return value
}

func (outbox *DaemonOutbox) start(run func()) {
	outbox.wait.Add(1)
	go func() {
		defer outbox.wait.Done()
		run()
	}()
}

// Drain has no timeout because abandoning an already-dispatched external
// Effect would manufacture ambiguity. Upgrade callers may impose their own
// wait and report a blocker, but the daemon process never force-retries it.
func (outbox *DaemonOutbox) Drain() { outbox.wait.Wait() }
