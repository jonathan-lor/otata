package tsnetnode

import (
	"context"
	"time"

	"tailscale.com/ipn"
)

type notificationWatcher interface {
	Next() (ipn.Notify, error)
	Close() error
}

func (n *embedded) Watch(ctx context.Context) (notificationWatcher, error) {
	lc, err := n.LocalClient()
	if err != nil {
		return nil, err
	}
	// Initial state and registration are atomic in the backend: an already
	// running node and changes during subscription setup are both observed.
	return lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyInitialHealthState)
}

// watchNode drains notifications independently of potentially slow certificate
// and page preparation. Coalescing wakeups is safe because the supervisor
// fetches a full snapshot; it does not reconstruct state from event deltas.
func watchNode(ctx context.Context, n node, changed chan<- struct{}, done chan<- struct{}, logf func(string, ...any)) {
	defer close(done)
	signal := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	for ctx.Err() == nil {
		w, err := n.Watch(ctx)
		if err == nil {
			signal() // Always resynchronize after reconnecting.
			for {
				var ev ipn.Notify
				ev, err = w.Next()
				if err != nil {
					// A lagging consumer receives a terminal error notification
					// followed by EOF. Reconnect and fetch current state.
					break
				}
				// DNS and certificate settings can change without a State transition.
				// SelfChange is also emitted for these control-plane map updates.
				if ev.State != nil || ev.SelfChange != nil || ev.Health != nil || ev.BrowseToURL != nil || ev.LoginFinished != nil || ev.ErrMessage != nil {
					signal()
				}
			}
			w.Close()
		}
		if ctx.Err() != nil {
			return
		}
		if logf != nil {
			logf("tsnet notifications interrupted: %v; reconnecting", err)
		}
		signal()
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
