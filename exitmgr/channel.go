package exitmgr

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// channel is one client's directory under the manager's data directory,
// and its IPC socket in the manager's runtime directory.
type channel struct {
	dir  string
	sock string
}

func (ch channel) confPath() string    { return filepath.Join(ch.dir, "node.conf") }
func (ch channel) keyPath() string     { return filepath.Join(ch.dir, "encryption-key") }
func (ch channel) cookiesPath() string { return filepath.Join(ch.dir, "cookies.json") }
func (ch channel) socketPath() string  { return ch.sock }

// ensureKey creates the channel's encryption key on first use and keeps it
// afterwards, so a client's links survive edits and restarts.
func (ch channel) ensureKey() error {
	if err := os.MkdirAll(ch.dir, 0o700); err != nil {
		return err
	}
	if b, err := os.ReadFile(ch.keyPath()); err == nil && strings.TrimSpace(string(b)) != "" {
		return nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	return writeFileAtomic(ch.keyPath(), []byte(base64.RawURLEncoding.EncodeToString(buf)+"\n"), 0o600)
}

// writeConf writes node.conf for cfg in the format deploy/node-install.sh
// writes, which the core reads with --config.
func (ch channel) writeConf(cfg ClientConfig) error {
	return writeFileAtomic(ch.confPath(), []byte(nodeConf(cfg, ch)), 0o600)
}

func nodeConf(cfg ClientConfig, ch channel) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# OpenFlux exit-panel channel %s (%s), written by the panel\n", cfg.ID, cfg.Name)
	fmt.Fprintf(&b, "Role = exit\nMode = %s\n", cfg.mode())
	fmt.Fprintf(&b, "EncryptionKeyFile = %s\n", ch.keyPath())
	fmt.Fprintf(&b, "CookieStore = %s\n", ch.cookiesPath())
	if cfg.Context != "" {
		fmt.Fprintf(&b, "URL = %s\n", cfg.Context)
	}
	names := carrierNames(cfg.Transports)
	for i, t := range cfg.Transports {
		fmt.Fprintf(&b, "\n[Transport %s]\nType = %s\nPriority = %d\n", names[i], t.Type, priorities[t.Type])
		if t.URL != "" {
			fmt.Fprintf(&b, "URL = %s\n", t.URL)
		}
	}
	if cfg.DirectPort > 0 {
		fmt.Fprintf(&b, "\n[Transport direct]\nType = direct\nPriority = %d\nListen = 0.0.0.0:%d\n", directPriority, cfg.DirectPort)
	}
	return b.String()
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
