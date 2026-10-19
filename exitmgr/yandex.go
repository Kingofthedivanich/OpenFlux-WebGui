package exitmgr

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/yandex"
)

// A Yandex sign-in for the whole panel: a Netscape cookies.txt every
// channel's core gets as --yandex-cookies-file. Yandex lets only a signed-in
// account open a document's editor, so vyandex needs one; and a server in a
// datacenter is asked for checks a signed-in account passes more often.
// Use an account of its own: its session sits on the server.

func (m *Manager) yandexCookiesPath() string {
	return filepath.Join(m.opts.DataDir, "yandex-cookies.txt")
}

// YandexCookies reports whether a sign-in is set, and when it was set.
func (m *Manager) YandexCookies() (bool, time.Time) {
	st, err := os.Stat(m.yandexCookiesPath())
	if err != nil {
		return false, time.Time{}
	}
	return true, st.ModTime()
}

// SetYandexCookies checks a cookies.txt the way the core reads it, keeps it
// root-only and restarts every channel with it.
func (m *Manager) SetYandexCookies(data []byte) error {
	data = toNetscape(bytes.TrimSpace(data))
	if !hasYandexSession(data) {
		return fmt.Errorf("no Session_id cookie for yandex.ru: export the cookies of a signed-in yandex.ru tab in Netscape cookies.txt format")
	}
	tmp, err := os.CreateTemp(m.opts.DataDir, ".yandex-cookies-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	probe := yandex.NewYandexVolgaTransport("https://docs.yandex.ru/edit/d/0000000000000000", transport.DefaultConfig())
	if err := probe.LoadCookieFile(name); err != nil {
		return err
	}
	if err := writeFileAtomic(m.yandexCookiesPath(), append(data, '\n'), 0o600); err != nil {
		return err
	}
	m.restartAll()
	return nil
}

// ClearYandexCookies removes the sign-in and restarts every channel without it.
func (m *Manager) ClearYandexCookies() error {
	if err := os.Remove(m.yandexCookiesPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	m.restartAll()
	return nil
}

// toNetscape takes what an operator can copy out of a browser: a cookies.txt
// (kept as it is), or a Cookie header ("Session_id=...; yandexuid=..."), which
// becomes a cookies.txt for .yandex.ru.
func toNetscape(data []byte) []byte {
	for _, line := range strings.Split(string(data), "\n") {
		if len(strings.Split(strings.TrimSuffix(line, "\r"), "\t")) == 7 {
			return data
		}
	}
	header := strings.TrimSpace(string(data))
	if i := strings.IndexByte(header, ':'); i >= 0 && strings.EqualFold(strings.TrimSpace(header[:i]), "cookie") {
		header = header[i+1:]
	}
	var b strings.Builder
	for _, pair := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || strings.ContainsAny(name+value, "\t\r\n") {
			continue
		}
		b.WriteString(".yandex.ru\tTRUE\t/\tTRUE\t0\t" + name + "\t" + value + "\n")
	}
	if b.Len() == 0 {
		return data
	}
	return []byte(strings.TrimSpace(b.String()))
}

// hasYandexSession reports a Session_id cookie for yandex.ru, the one a
// signed-in browser has: a cookies.txt of a signed-out tab would load fine
// and sign nothing in.
func hasYandexSession(data []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), 1024*1024)
	for sc.Scan() {
		f := strings.Split(strings.TrimSuffix(sc.Text(), "\r"), "\t")
		if len(f) != 7 {
			continue
		}
		domain := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(f[0], "#HttpOnly_"), "."))
		if (domain == "yandex.ru" || strings.HasSuffix(domain, ".yandex.ru")) && f[5] == "Session_id" && f[6] != "" {
			return true
		}
	}
	return false
}

// restartAll restarts every channel, so each core picks up the sign-in.
func (m *Manager) restartAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.clients {
		if e.proc != nil {
			e.proc.stop()
		}
		m.clients[id] = m.launch(e.cfg)
	}
}

// portBindable fails when something on this host already listens on port:
// the channel's direct transport would retry binding forever ("address
// already in use") and its clients would get a dead backup.
func portBindable(port int) error {
	if port == 0 {
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("direct port %d is busy on this server (another program listens on it): pick another one", port)
	}
	ln.Close()
	return nil
}
