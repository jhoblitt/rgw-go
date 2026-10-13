package parity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// writeAtomic replaces the file at path with data, keeping its mode,
// through a file beside it renamed into place, so that a failed write
// leaves the old file whole.
func writeAtomic(file string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", file, err)
	}
	tmp := f.Name()
	fill := func() error {
		if info, serr := os.Stat(file); serr == nil {
			if cerr := f.Chmod(info.Mode().Perm()); cerr != nil {
				return cerr
			}
		}
		_, werr := f.Write(data)
		return werr
	}
	if err := fill(); err != nil {
		return fmt.Errorf("writing %s: %w", file, errors.Join(err, f.Close(), os.Remove(tmp)))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", file, errors.Join(err, os.Remove(tmp)))
	}
	if err := os.Rename(tmp, file); err != nil {
		return fmt.Errorf("writing %s: %w", file, errors.Join(err, os.Remove(tmp)))
	}
	return nil
}

// writeLedger replaces the file at path with l, so that a failed write
// leaves the old ledger whole.
func writeLedger(path string, l *Ledger) error {
	data, err := MarshalLedger(l)
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}
