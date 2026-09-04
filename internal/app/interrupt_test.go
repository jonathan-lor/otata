package app

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/storage"
)

// The first signal cancels the build's context and is remembered, so Publish
// can report the stop with the shell's exit status for it. The process itself
// survives: the signal is delivered to this test binary, and Notify is what
// keeps it from being fatal. A second signal would exit at once, so only one
// is sent.
func TestFirstSignalCancelsTheBuildAndIsReported(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: store}
	ctx, interrupted, stop := a.onInterrupt("app", "https://host/otata")
	defer stop()

	if interrupted() != nil {
		t.Fatal("interrupted before any signal")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the build's context was not cancelled by the signal")
	}
	if got := interrupted(); got != syscall.SIGTERM {
		t.Errorf("interrupted() = %v, want SIGTERM", got)
	}
}

// The exit status follows the shell's convention for a signalled process,
// per signal, rather than 130 for everything.
func TestSignalExit(t *testing.T) {
	cases := map[os.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143, syscall.SIGHUP: 129}
	for sig, want := range cases {
		if got := signalExit(sig); got != want {
			t.Errorf("signalExit(%s) = %d, want %d", signalName(sig), got, want)
		}
	}
	if got := signalName(syscall.SIGTERM); got != "SIGTERM" {
		t.Errorf("signalName = %q", got)
	}
}
