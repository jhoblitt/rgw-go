package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// IsFile reports whether path names a regular file.
func IsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// OpenLog opens path for appending, creating it.
func OpenLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the path is in the run directory
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return f, nil
}

// WriteJSON writes v to path as indented JSON.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", filepath.Base(path), err)
	}
	err = os.WriteFile(path, append(data, '\n'), 0o600) //nolint:gosec // the path is in the run directory
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
