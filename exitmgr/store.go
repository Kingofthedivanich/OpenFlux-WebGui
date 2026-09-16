package exitmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store persists a set of ClientConfigs to a JSON file, so registered
// clients survive a process restart.
type Store struct {
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

// Load reads the store's file. A missing file is not an error (treated as
// an empty set) so the first run doesn't need the file pre-created.
func (s *Store) Load() ([]ClientConfig, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var cfgs []ClientConfig
	if err := json.Unmarshal(data, &cfgs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return cfgs, nil
}

// Save writes cfgs to the store's file atomically (write to a temp file in
// the same directory, then rename) so a crash mid-write can't corrupt the
// existing file.
func (s *Store) Save(cfgs []ClientConfig) error {
	data, err := json.MarshalIndent(cfgs, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".exitmgr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}
