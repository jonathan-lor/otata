package app

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/storage"
)

// deadPID is a pid that is certainly not running, like a child that has already been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot spawn a child to obtain a dead pid")
	}
	return cmd.Process.Pid
}

// One rule decides whether a marker still means a build is running, and both
// the publish lock and doctor's reconciliation use it.
func TestMarkerStale(t *testing.T) {
	now := time.Now()
	live := artifact.Building{Slug: "a", Started: now.Add(-time.Minute), PID: os.Getpid()}
	if markerStale(live, now) {
		t.Error("a marker held by a live process was called stale")
	}
	dead := artifact.Building{Slug: "a", Started: now.Add(-time.Minute), PID: deadPID(t)}
	if !markerStale(dead, now) {
		t.Error("a marker whose process is gone was believed")
	}
	// PIDs are reused after a reboot. A marker older than any build could be
	// is stale even when its pid happens to be alive again.
	reused := artifact.Building{Slug: "a", Started: now.Add(-maxBuildAge - time.Minute), PID: os.Getpid()}
	if !markerStale(reused, now) {
		t.Error("an ancient marker with a (reused) live pid was believed forever")
	}
	// A marker with no pid predates the check: only its age can be judged.
	legacyFresh := artifact.Building{Slug: "a", Started: now.Add(-time.Hour)}
	legacyOld := artifact.Building{Slug: "a", Started: now.Add(-maxBuildAge - time.Hour)}
	if markerStale(legacyFresh, now) || !markerStale(legacyOld, now) {
		t.Error("a pid-less marker is judged by age alone")
	}
}

// A process that exists but refuses the probe signal (another user's, which
// is what pid 1 is to anyone but root) is alive. Reading the refusal as
// "gone" would clear a marker whose publish is still running.
func TestProcessAliveTreatsAnotherUsersProcessAsAlive(t *testing.T) {
	if !processAlive(1) {
		t.Error("pid 1 was judged dead")
	}
	if processAlive(deadPID(t)) {
		t.Error("a reaped child was judged alive")
	}
	if processAlive(0) || processAlive(-1) {
		t.Error("a non-pid was judged alive")
	}
}

// A second publish of a slug that a live publish holds is refused with a code
// an agent can wait on; one whose holder is gone is taken over.
func TestClaimBuildRefusesALiveHolderAndTakesOverADeadOne(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: store}
	slug := "app"

	held := artifact.Building{Slug: slug, Started: time.Now(), PID: os.Getpid()}
	if _, claimed, err := store.ClaimBuilding(held); err != nil || !claimed {
		t.Fatalf("setup claim: %v %v", claimed, err)
	}
	err = a.claimBuild(artifact.Building{Slug: slug, Started: time.Now(), PID: os.Getpid()})
	if err == nil {
		t.Fatal("claimed a slug another live publish holds")
	}
	if f := cli.AsFailure(err); f.Code != cli.CodeBuildInProgress {
		t.Errorf("code = %q, want %q (%s)", f.Code, cli.CodeBuildInProgress, f.Message)
	}
	markers, _ := store.Building()
	if markers[slug].PID != os.Getpid() {
		t.Errorf("the holder's marker was disturbed: %+v", markers[slug])
	}

	// The holder dies without clearing; the next publish takes over.
	if err := store.ClearBuilding(slug); err != nil {
		t.Fatal(err)
	}
	stale := artifact.Building{Slug: slug, Started: time.Now(), PID: deadPID(t)}
	if _, claimed, err := store.ClaimBuilding(stale); err != nil || !claimed {
		t.Fatalf("setup stale: %v %v", claimed, err)
	}
	mine := artifact.Building{Slug: slug, Started: time.Now(), PID: os.Getpid(), Config: "Debug"}
	if err := a.claimBuild(mine); err != nil {
		t.Fatalf("could not take over a dead holder's marker: %v", err)
	}
	markers, _ = store.Building()
	if markers[slug].PID != os.Getpid() || markers[slug].Config != "Debug" {
		t.Errorf("marker after takeover = %+v, want mine", markers[slug])
	}
}

// Forget while a live build holds the slug would be undone by that build
// finishing, so it is refused with the same code.
func TestForgetRefusesWhileBuilding(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: store}
	if err := store.PutRecord(artifact.Record{Slug: "app", BuiltAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimBuilding(artifact.Building{Slug: "app", Started: time.Now(), PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	_, err = a.Forget("app")
	if f := cli.AsFailure(err); err == nil || f.Code != cli.CodeBuildInProgress {
		t.Fatalf("forget during a live build: err=%v", err)
	}
	if _, ok, _ := store.Record("app"); !ok {
		t.Error("the record was removed despite the refusal")
	}
}
