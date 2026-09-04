//go:build unix

package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A cancelled build must take every process the toolchain started with it,
// not only the parent. The stand-in for xcodebuild is a shell whose
// background child keeps writing a heartbeat; if only the shell died, the
// child would go on writing after runLogged returned, which is exactly what
// an orphaned swift-frontend did in the work directory.
func TestCancelKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	beat := filepath.Join(dir, "beat")
	log, err := os.Create(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Cancel once the grandchild is provably running.
		for {
			if info, err := os.Stat(beat); err == nil && info.Size() > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	start := time.Now()
	err = runLogged(ctx, log, "sh", "-c", "(while :; do echo x >> "+beat+"; sleep 0.05; done) & wait")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runLogged returned %v, want the context's cancellation", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("cancellation took %s; the parent was waited on, not signalled", took)
	}

	// The heartbeat must have stopped. A short grace covers signal delivery.
	time.Sleep(200 * time.Millisecond)
	before, _ := os.Stat(beat)
	time.Sleep(300 * time.Millisecond)
	after, _ := os.Stat(beat)
	if before == nil || after == nil {
		t.Fatal("no heartbeat file was written")
	}
	if after.Size() != before.Size() {
		t.Errorf("the grandchild kept writing after cancellation (%d -> %d bytes): only the parent was killed",
			before.Size(), after.Size())
	}
}

// An uncancelled run is unaffected by any of this.
func TestRunLoggedReportsTheCommandsOwnFailure(t *testing.T) {
	log, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	err = runLogged(context.Background(), log, "sh", "-c", "exit 3")
	if err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want the command's own exit error", err)
	}
	if err := runLogged(context.Background(), log, "sh", "-c", "exit 0"); err != nil {
		t.Errorf("a successful command returned %v", err)
	}
}
