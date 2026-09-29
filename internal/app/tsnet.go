package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/server"
	"github.com/jonathan-lor/otata/internal/transport"
	"github.com/jonathan-lor/otata/internal/tsnetnode"
)

func (a *App) serveTSNet(handler http.Handler) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ln, err := server.Listen(a.Config.Port)
	if err != nil {
		return cli.Failf(cli.CodeServerDown, "could not bind 127.0.0.1:%d: %v", a.Config.Port, err)
	}
	defer ln.Close()
	local := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	defer local.Close()
	localDone := make(chan error, 1)
	nodeDone := make(chan error, 1)
	// A successful local identity probe must imply the control API exists.
	// The callback runs before tsnet initializes, preserving visibility during
	// enrollment and slow startup without exposing a missing control receipt.
	go func() {
		nodeDone <- tsnetnode.Run(ctx, tsnetnode.Options{
			Dir:          a.tsnetDir(),
			Identity:     a.tsnetIdentity(a.Config),
			Handler:      handler,
			Prepare:      a.Reindex,
			ControlReady: func() { go func() { localDone <- local.Serve(ln) }() },
			Logf:         func(format string, args ...any) { cli.Line(os.Stderr, "%s", fmt.Sprintf(format, args...)) },
		})
	}()
	select {
	case <-ctx.Done():
		// Keep the local identity listener alive until the node has released
		// its state lock, so stop/start cannot race against that release.
		err = <-nodeDone
	case err = <-localDone:
		cancel()
		<-nodeDone
	case err = <-nodeDone:
		cancel()
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (a *App) LoginTransport() error {
	tr, err := a.Transport()
	if err != nil {
		return err
	}
	t, ok := tr.(*transport.TSNet)
	if !ok {
		return cli.Fail(cli.CodeInvalidArgs, "transport login requires Tailscale; select it with 'otata transport use tailscale'")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := t.Login(ctx); err != nil {
		return cli.Failf(cli.CodeTransportDown, "%v", err)
	}
	for range 20 {
		s := t.Status(a.Config.Port)
		if s.Ready || s.AuthURL != "" || (s.State != "starting" && s.State != "needs_login" && s.State != "connecting") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}
