// Package atomicfile replaces a file in one step: readers see the old or the new content, never a mix.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to a temp file in path's directory, syncs it and renames it over path. On any error the
// temp file is removed and an existing path is left untouched.
func Write(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp file in %s: %w; make the directory writable", dir, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(f.Name()))
		}
	}()
	if _, err = f.Write(data); err != nil {
		return errors.Join(fmt.Errorf("atomicfile: write %s: %w", f.Name(), err), f.Close())
	}
	if err = f.Chmod(perm); err != nil {
		return errors.Join(fmt.Errorf("atomicfile: chmod %s: %w", f.Name(), err), f.Close())
	}
	if err = f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("atomicfile: sync %s: %w", f.Name(), err), f.Close())
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("atomicfile: close %s: %w", f.Name(), err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("atomicfile: rename %s to %s: %w", f.Name(), path, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("atomicfile: open %s: %w", dir, err)
	}
	return errors.Join(d.Sync(), d.Close())
}
