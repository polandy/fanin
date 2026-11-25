package shadow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestProcLocksHeld(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, alphaLock)
	if err := os.WriteFile(lock, []byte("a 1 7 2026-01-02 03:06\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	sys := info.Sys().(*syscall.Stat_t)
	id := lockID(uint64(sys.Dev), sys.Ino)
	other := lockID(uint64(sys.Dev)+1, sys.Ino)
	cases := []struct {
		name  string
		locks string
		want  bool
	}{
		{"SH-3_flock_on_the_file_is_held", fmt.Sprintf("1: POSIX  ADVISORY  WRITE 10 %s 0 EOF\n"+
			"2: FLOCK  ADVISORY  WRITE 4242 %s 0 EOF\n", other, id), true},
		{"SH-3_a_blocked_waiter_alone_is_not_held", fmt.Sprintf("2: -> FLOCK  ADVISORY  WRITE 4243 %s 0 EOF\n", id),
			false},
		{"SH-3_same_inode_on_another_device_is_free", fmt.Sprintf("2: FLOCK  ADVISORY  WRITE 4242 %s 0 EOF\n", other),
			false},
		{"SH-3_posix_lock_is_not_a_flock", fmt.Sprintf("1: POSIX  ADVISORY  WRITE 10 %s 0 EOF\n", id), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc := filepath.Join(t.TempDir(), "locks")
			if err := os.WriteFile(proc, []byte(tc.locks), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ProcLocks{Dir: dir, Locks: proc}.Held(alphaLock)
			if err != nil || got != tc.want {
				t.Errorf("Held = %t, %v; want %t", got, err, tc.want)
			}
		})
	}
	t.Run("SH-3_lock_id_formats_like_proc_locks", func(t *testing.T) {
		if got := lockID(0xfe01, 12345); got != "fe:01:12345" {
			t.Errorf("lockID = %q, want fe:01:12345", got)
		}
		if got := lockID(0x1f, 7); got != "00:1f:7" {
			t.Errorf("lockID = %q, want 00:1f:7", got)
		}
	})
	t.Run("SH-4_unreadable_proc_locks", func(t *testing.T) {
		_, err := ProcLocks{Dir: dir, Locks: filepath.Join(dir, "none")}.Held(alphaLock)
		if !errors.Is(err, ErrUnreadable) {
			t.Errorf("Held: %v, want ErrUnreadable", err)
		}
	})
}
