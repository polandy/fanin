package store

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/safego"
)

const (
	lease       = 10 * time.Minute
	lockTimeout = time.Hour
)

func req(holder string) LockRequest {
	return LockRequest{Project: project, Holder: holder, Lease: lease}
}

func goWait(ctx context.Context, s *Store, r LockRequest) <-chan error {
	done := make(chan error, 1)
	safego.Go(ctx, slog.New(slog.DiscardHandler), func(ctx context.Context) { done <- s.WaitLock(ctx, r, lockTimeout) })
	return done
}

func TestLock(t *testing.T) {
	t.Run("MG-1_FP-1_second_acquire_is_ErrLockHeld", func(t *testing.T) {
		s, _ := newStore(t)
		ctx := context.Background()
		if err := s.AcquireLock(ctx, req("task-a")); err != nil {
			t.Fatalf("first acquire: %v", err)
		}
		for _, holder := range []string{"task-b", "task-a"} {
			err := s.AcquireLock(ctx, req(holder))
			var held *LockHeldError
			if !errors.Is(err, ErrLockHeld) || !errors.As(err, &held) || held.Holder != "task-a" {
				t.Errorf("acquire by %s: %v, want ErrLockHeld naming task-a", holder, err)
			}
			if errors.Is(err, ErrLockTimeout) {
				t.Errorf("acquire by %s: %v also matches ErrLockTimeout", holder, err)
			}
			want := `store: lock of project "proj-a" held by "task-a" until 2026-03-04T05:16:07Z; ` +
				"wait or release that holder"
			if err != nil && err.Error() != want {
				t.Errorf("message %q, want %q", err.Error(), want)
			}
		}
		other := LockRequest{Project: "proj-b", Holder: "task-b", Lease: lease}
		if err := s.AcquireLock(ctx, other); err != nil {
			t.Errorf("lock of another project: %v, want nil", err)
		}
	})

	t.Run("MG-2_waiting_acquire_succeeds_after_release", func(t *testing.T) {
		s, _ := newStore(t)
		ctx := context.Background()
		if err := s.AcquireLock(ctx, req("task-a")); err != nil {
			t.Fatal(err)
		}
		waiting := make(chan struct{}, 1)
		s.waiting = func() { waiting <- struct{}{} }
		done := goWait(ctx, s, req("task-b"))
		<-waiting
		if err := s.ReleaseLock(ctx, project, "task-a"); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("WaitLock: %v, want nil after the release", err)
		}
		if err := s.AcquireLock(ctx, req("task-c")); !errors.Is(err, ErrLockHeld) {
			t.Errorf("acquire after the wait: %v, want ErrLockHeld (task-b holds it)", err)
		}
	})

	t.Run("MG-2_wait_timeout_names_holder_and_duration", func(t *testing.T) {
		s, clk := newStore(t)
		ctx := context.Background()
		long := LockRequest{Project: project, Holder: "task-a", Lease: 2 * lockTimeout}
		if err := s.AcquireLock(ctx, long); err != nil {
			t.Fatal(err)
		}
		waiting := make(chan struct{}, 1)
		s.waiting = func() { waiting <- struct{}{} }
		done := goWait(ctx, s, req("task-b"))
		<-waiting
		clk.Advance(lockTimeout)
		err := <-done
		var held *LockHeldError
		if !errors.Is(err, ErrLockTimeout) || !errors.Is(err, ErrLockHeld) || !errors.As(err, &held) {
			t.Fatalf("WaitLock: %v, want a timeout LockHeldError", err)
		}
		if held.Holder != "task-a" || held.Waited != lockTimeout {
			t.Errorf("holder %q waited %s, want task-a %s", held.Holder, held.Waited, lockTimeout)
		}
		want := `store: lock of project "proj-a" held for over 1h0m0s by "task-a" (since 2026-03-04T05:06:07Z); ` +
			"end or release that holder"
		if err.Error() != want {
			t.Errorf("message %q, want %q", err.Error(), want)
		}
	})

	t.Run("MG-2_wait_ends_with_ctx", func(t *testing.T) {
		s, _ := newStore(t)
		if err := s.AcquireLock(context.Background(), req("task-a")); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.waiting = cancel
		if err := s.WaitLock(ctx, req("task-b"), lockTimeout); !errors.Is(err, context.Canceled) {
			t.Errorf("WaitLock: %v, want context.Canceled", err)
		}
	})

	t.Run("MG-3_expired_lease_frees_the_lock", func(t *testing.T) {
		s, clk := newStore(t)
		ctx := context.Background()
		if err := s.AcquireLock(ctx, req("task-a")); err != nil {
			t.Fatal(err)
		}
		clk.Advance(lease - time.Second)
		if err := s.AcquireLock(ctx, req("task-b")); !errors.Is(err, ErrLockHeld) {
			t.Fatalf("acquire before expiry: %v, want ErrLockHeld", err)
		}
		clk.Advance(time.Second)
		if err := s.AcquireLock(ctx, req("task-b")); err != nil {
			t.Fatalf("acquire after expiry: %v, want nil", err)
		}
		if err := s.RenewLock(ctx, req("task-a")); !errors.Is(err, ErrLockNotHeld) {
			t.Errorf("renew by the expired holder: %v, want ErrLockNotHeld", err)
		}
	})

	t.Run("MG-3_wait_takes_an_expiring_lease", func(t *testing.T) {
		s, clk := newStore(t)
		ctx := context.Background()
		short := LockRequest{Project: project, Holder: "task-a", Lease: LockPoll}
		if err := s.AcquireLock(ctx, short); err != nil {
			t.Fatal(err)
		}
		waiting := make(chan struct{}, 1)
		s.waiting = func() { waiting <- struct{}{} }
		done := goWait(ctx, s, req("task-b"))
		<-waiting
		clk.Advance(LockPoll)
		if err := <-done; err != nil {
			t.Fatalf("WaitLock: %v, want nil once the lease expired", err)
		}
	})

	t.Run("MG-3_renew_keeps_the_lease", func(t *testing.T) {
		s, clk := newStore(t)
		ctx := context.Background()
		if err := s.AcquireLock(ctx, req("task-a")); err != nil {
			t.Fatal(err)
		}
		clk.Advance(lease - time.Second)
		if err := s.RenewLock(ctx, req("task-a")); err != nil {
			t.Fatal(err)
		}
		clk.Advance(lease - time.Second)
		if err := s.AcquireLock(ctx, req("task-b")); !errors.Is(err, ErrLockHeld) {
			t.Errorf("acquire within the renewed lease: %v, want ErrLockHeld", err)
		}
	})
}

func TestLockFailures(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for _, r := range []LockRequest{{Holder: "h", Lease: lease}, {Project: project, Lease: lease}, {Project: project, Holder: "h"}} {
		if err := s.AcquireLock(ctx, r); !errors.Is(err, ErrInvalid) {
			t.Errorf("AcquireLock(%+v): %v, want ErrInvalid", r, err)
		}
		if err := s.RenewLock(ctx, r); !errors.Is(err, ErrInvalid) {
			t.Errorf("RenewLock(%+v): %v, want ErrInvalid", r, err)
		}
	}
	if err := s.WaitLock(ctx, LockRequest{}, lockTimeout); !errors.Is(err, ErrInvalid) {
		t.Errorf("WaitLock: %v, want ErrInvalid", err)
	}
	if err := s.AcquireLock(ctx, req("task-a")); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLock(ctx, project, "task-b"); !errors.Is(err, ErrLockNotHeld) {
		t.Errorf("release by another holder: %v, want ErrLockNotHeld", err)
	}
	if err := s.AcquireLock(ctx, req("task-b")); !errors.Is(err, ErrLockHeld) {
		t.Errorf("acquire after a refused release: %v, want ErrLockHeld", err)
	}
	if err := s.ReleaseLock(ctx, project, "task-a"); err != nil {
		t.Errorf("release by the holder: %v", err)
	}
	if err := s.ReleaseLock(ctx, project, "task-a"); !errors.Is(err, ErrLockNotHeld) {
		t.Errorf("second release: %v, want ErrLockNotHeld", err)
	}
}
