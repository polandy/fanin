package shadow

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ProcLocksPath is the kernel's table of file locks.
const ProcLocksPath = "/proc/locks"

// procLockFields are the fields of a held /proc/locks line: "<n>: FLOCK ADVISORY WRITE <pid> <maj:min:ino> ...".
const (
	procLockFields = 6
	procLockKind   = "FLOCK"
)

// ProcLocks is the Probe of a real state directory: it looks the file's device and inode up in the Locks table, as
// lib.sh's lock_pids does.
type ProcLocks struct {
	Dir   string
	Locks string
}

// Held reports whether a process holds a flock on name in p.Dir; a missing file is free.
func (p ProcLocks) Held(name string) (bool, error) {
	path := filepath.Join(p.Dir, name)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: stat %s: %w", ErrUnreadable, path, err)
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("%w: stat %s has no device and inode; run fanind on Linux", ErrUnreadable, path)
	}
	data, err := os.ReadFile(p.Locks)
	if err != nil {
		return false, fmt.Errorf("%w: read %s: %w; fanind needs the kernel lock table", ErrUnreadable, p.Locks, err)
	}
	id := lockID(uint64(sys.Dev), sys.Ino) //nolint:unconvert // Dev is not uint64 on every platform
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) >= procLockFields && f[1] == procLockKind && f[5] == id {
			return true, nil
		}
	}
	return false, nil
}

// lockID formats a device and inode like /proc/locks: "<major hex>:<minor hex>:<inode>".
func lockID(dev, ino uint64) string {
	major := (dev>>8)&0xfff | (dev>>32)&^0xfff
	minor := dev&0xff | (dev>>12)&^0xff
	return fmt.Sprintf("%02x:%02x:%d", major, minor, ino)
}
