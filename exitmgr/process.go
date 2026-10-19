package exitmgr

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport/ipc"
)

const (
	logLines       = 300
	restartMin     = 2 * time.Second
	restartMax     = time.Minute
	healthyRun     = time.Minute // a run this long resets the restart backoff
	stopGrace      = 5 * time.Second
	ipcRetry       = time.Second
	statusInterval = 3 * time.Second // the core reports every second; silence this long is "not connected"
)

// Check is a sign-in or captcha a channel's carrier waits for: the core asks
// for it over IPC and the carrier stays down until someone passes it.
type Check struct {
	Transport string `json:"transport"`
	URL       string `json:"url,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// proc keeps one channel's core process running: it restarts the process
// with a backoff when it exits, keeps its last log lines and the share link
// it printed, and follows its IPC status.
type proc struct {
	argv   []string
	env    []string
	dir    string
	sock   string
	onLink func(string) // a new share link was printed; may be nil

	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}

	mu        sync.Mutex
	status    string
	lastErr   string
	startedAt time.Time
	restarts  int
	link      string
	logs      []string
	st        ipc.StatusPayload
	statusAt  time.Time
	check     *Check
}

func startProc(argv, env []string, dir, sock string, onLink func(string)) *proc {
	p := &proc{
		argv:   argv,
		env:    env,
		dir:    dir,
		sock:   sock,
		onLink: onLink,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
		status: "starting",
	}
	go p.run()
	return p
}

// stop ends the process and waits for it; the proc does not restart again.
func (p *proc) stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	<-p.done
}

func (p *proc) stopping() bool {
	select {
	case <-p.stopCh:
		return true
	default:
		return false
	}
}

func (p *proc) run() {
	defer close(p.done)
	backoff := restartMin
	for {
		began := time.Now()
		err := p.once()
		if p.stopping() {
			p.set(func() { p.status = "stopped" })
			return
		}
		if time.Since(began) >= healthyRun {
			backoff = restartMin
		}
		msg := "exited"
		if err != nil {
			msg = "exited: " + err.Error()
		}
		p.set(func() {
			p.status = "restarting"
			p.lastErr = msg
			p.st = ipc.StatusPayload{}
		})
		select {
		case <-p.stopCh:
			p.set(func() { p.status = "stopped" })
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > restartMax {
			backoff = restartMax
		}
		p.set(func() { p.restarts++ })
	}
}

// once runs the process until it exits or the proc is stopped.
func (p *proc) once() error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd := exec.Command(p.argv[0], p.argv[1:]...)
	cmd.Dir = p.dir
	cmd.Env = p.env
	cmd.Stdout = w
	cmd.Stderr = w
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		p.set(func() { p.lastErr = "start: " + err.Error() })
		return err
	}
	w.Close() // the child holds its own copy; EOF on r once it exits
	p.set(func() {
		p.status = "running"
		p.startedAt = time.Now()
		p.check = nil
	})

	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			p.line(sc.Text())
		}
		r.Close()
	}()
	ipcDone := make(chan struct{})
	go p.followIPC(ipcDone)

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err = <-waited:
	case <-p.stopCh:
		err = stopProcess(cmd.Process, waited, stopGrace)
	}
	close(ipcDone)
	<-scanned
	return err
}

// line takes one line of the core's output.
func (p *proc) line(s string) {
	s = strings.TrimRight(s, "\r")
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "openflux://") {
		// printShare puts the bare link on a line of its own; it is printed
		// again whenever it changes (cupsonline/avito rooms made later).
		changed := false
		p.set(func() { changed, p.link = p.link != t, t })
		if changed && p.onLink != nil {
			p.onLink(t)
		}
		return
	}
	if isQRLine(t) {
		return
	}
	p.set(func() {
		p.logs = append(p.logs, s)
		if len(p.logs) > logLines {
			p.logs = append(p.logs[:0:0], p.logs[len(p.logs)-logLines:]...)
		}
	})
}

// isQRLine reports a line of the half-block QR code printShare draws.
func isQRLine(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch r {
		case ' ', '█', '▀', '▄':
		default:
			return false
		}
	}
	return true
}

// followIPC reads the core's IPC status until done is closed, reconnecting
// while the process starts up.
func (p *proc) followIPC(done chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		conn, err := net.Dial("unix", p.sock)
		if err != nil {
			select {
			case <-done:
				return
			case <-time.After(ipcRetry):
			}
			continue
		}
		closed := make(chan struct{})
		go func() {
			select {
			case <-done:
			case <-closed:
			}
			conn.Close()
		}()
		p.readIPC(conn)
		close(closed)
	}
}

func (p *proc) readIPC(conn net.Conn) {
	for {
		typ, payload, err := ipc.ReadFrame(conn)
		if err != nil {
			return
		}
		switch typ {
		case ipc.MsgStatus:
			var st ipc.StatusPayload
			if ipc.DecodeJSON(payload, &st) == nil {
				p.set(func() {
					p.st = st
					p.statusAt = time.Now()
					if st.Connected {
						p.check = nil
					}
				})
			}
		case ipc.MsgCookiesRequest:
			var req ipc.CookiesRequestPayload
			if ipc.DecodeJSON(payload, &req) == nil {
				p.set(func() { p.check = &Check{Transport: req.Transport, URL: req.URL, Reason: req.Reason} })
			}
		}
	}
}

func (p *proc) set(f func()) {
	p.mu.Lock()
	f()
	p.mu.Unlock()
}

// procState is a snapshot of a proc for the client list.
type procState struct {
	Status    string
	Error     string
	StartedAt time.Time
	Restarts  int
	Link      string
	Connected bool
	BytesIn   uint64
	BytesOut  uint64
	Active    []string
	Check     *Check
}

func (p *proc) state() procState {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := procState{
		Status:    p.status,
		Error:     p.lastErr,
		StartedAt: p.startedAt,
		Restarts:  p.restarts,
		Link:      p.link,
		BytesIn:   p.st.BytesIn,
		BytesOut:  p.st.BytesOut,
		Check:     p.check,
	}
	if p.status == "running" {
		s.Error = ""
		s.Connected = p.st.Connected && time.Since(p.statusAt) < statusInterval
	}
	if s.Connected {
		s.Active = append([]string(nil), p.st.ActiveAll...)
		if len(s.Active) == 0 && p.st.Active != "" {
			s.Active = []string{p.st.Active}
		}
	}
	return s
}

func (p *proc) logTail() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.logs...)
}
