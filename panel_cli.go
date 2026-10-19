package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/p1neappleXpress/OpenFlux/exitmgr"
	"github.com/p1neappleXpress/OpenFlux/panel"
	"github.com/p1neappleXpress/OpenFlux/telegrambot"
)

const roleExitPanel = "exit-panel"

// isExitPanel reports whether the command line asks for --role=exit-panel,
// which runs before (and instead of) the core's own flag set.
func isExitPanel(args []string) bool {
	for i, a := range args {
		switch a {
		case "--role=" + roleExitPanel, "-role=" + roleExitPanel, "-r=" + roleExitPanel:
			return true
		case "--role", "-role", "-r":
			if i+1 < len(args) && args[i+1] == roleExitPanel {
				return true
			}
		}
	}
	return false
}

// runExitPanel runs the exit panel: a login-gated web UI that adds and
// removes clients, each an exit channel (its own key, documents and core
// process) under --panel-data.
func runExitPanel(args []string) int {
	fs := flag.NewFlagSet("exit-panel", flag.ContinueOnError)
	fs.String("role", roleExitPanel, "exit-panel")
	addr := fs.String("panel-addr", "127.0.0.1:8088", "Address the web panel listens on. Keep it on loopback and reach it over an SSH tunnel")
	user := fs.String("panel-user", "", "Panel login (required)")
	pass := fs.String("panel-pass", "", "Panel password (required)")
	data := fs.String("panel-data", "openflux-panel", "Directory with the client list and every client's channel (keys, node.conf, cookies)")
	yandexTokenFile := fs.String("yandex-token-file", "", "Optional: file the panel keeps a Yandex OAuth token (disk.write) in, to create vyandex documents itself")
	shareHost := fs.String("share-host", "", "Address clients dial for a client's direct port. Default: this host's first public IPv4")
	allowPrivate := fs.Bool("allow-private", false, "Let clients reach this host's loopback, private networks and cloud metadata (refused by default)")
	debug := fs.Int("debug", 0, "Debug level passed to every channel's core (0-3)")
	botToken := fs.String("telegram-bot-token", "", "Optional: Telegram bot token (from @BotFather) for the admin bot; prefer --telegram-bot-token-file")
	botTokenFile := fs.String("telegram-bot-token-file", "", "Optional: file holding the Telegram bot token, so it stays out of the process list")
	botAdmins := fs.String("telegram-admin-ids", "", "Comma-separated Telegram user ids allowed to use the bot (required with a bot token)")
	// The old WebGui panel's Noise key flag, accepted and ignored so a
	// systemd unit written for it still starts this binary: deploy/update.sh
	// swaps the binary before openflux-ctl rewrites the unit, and rolls back
	// if the service does not come up.
	legacy := map[string]*string{"panel-key-file": fs.String("panel-key-file", "", "Ignored: a flag of the old WebGui panel")}
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "openflux --role=exit-panel --panel-user=U --panel-pass=P [options]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(expandShortFlags(append([]string{os.Args[0]}, args...))[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *user == "" || *pass == "" {
		log.Printf("--role=exit-panel requires --panel-user and --panel-pass")
		return 2
	}
	for name, v := range legacy {
		if *v != "" {
			log.Printf("warning: --%s is a flag of the old panel and is ignored; run openflux-ctl (Reconfigure) to rewrite the unit", name)
		}
	}
	// The old panel kept its clients in a JSON file (openflux-clients.json);
	// this one needs a directory. Use one beside that file: the old clients
	// are not carried over (their Noise keys do not apply here).
	if st, err := os.Stat(*data); err == nil && !st.IsDir() {
		dir := filepath.Join(filepath.Dir(*data), "openflux-panel")
		log.Printf("warning: --panel-data=%s is the old panel's client file; using %s instead (old clients are not carried over: add them again)", *data, dir)
		*data = dir
	}

	token := *botToken
	if *botTokenFile != "" {
		b, err := os.ReadFile(*botTokenFile)
		if err != nil {
			log.Printf("--telegram-bot-token-file: %v", err)
			return 2
		}
		token = strings.TrimSpace(string(b))
	}
	var admins []int64
	if token != "" {
		ids, err := telegrambot.ParseAdminIDs(*botAdmins)
		if err != nil {
			log.Printf("--telegram-admin-ids: %v (required with a bot token: only these users may use the bot)", err)
			return 2
		}
		admins = ids
	}

	var extra []string
	if *shareHost != "" {
		extra = append(extra, "--share-host", *shareHost)
	}
	if *allowPrivate {
		extra = append(extra, "--allow-private")
		log.Printf("warning: --allow-private: clients may reach this host's private networks and cloud metadata")
	}
	if *debug > 0 {
		extra = append(extra, "--debug", strconv.Itoa(*debug))
	}
	mgr, err := exitmgr.New(exitmgr.Options{DataDir: *data, ExtraArgs: extra})
	if err != nil {
		log.Printf("panel: %v", err)
		return 1
	}
	if err := mgr.Load(); err != nil {
		log.Printf("panel: load clients: %v", err)
		return 1
	}
	log.Printf("Exit panel: %d client(s) from %s", len(mgr.List()), *data)

	web := panel.NewServer(mgr, *user, *pass, *yandexTokenFile)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           web,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if host, _, _ := strings.Cut(*addr, ":"); host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		log.Printf("warning: --panel-addr=%s is reachable from outside; the panel hands out client keys over plain HTTP, keep it on 127.0.0.1", *addr)
	}

	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	log.Printf("Admin panel: http://%s (login required)", *addr)

	botCtx, stopBot := context.WithCancel(context.Background())
	defer stopBot()
	if token != "" {
		go telegrambot.New(token, admins, mgr, web).Run(botCtx)
	}

	sigCh := make(chan os.Signal, 1)
	notifySignals(sigCh)
	code := 0
	select {
	case <-sigCh:
		log.Printf("Shutting down the panel and its channels...")
	case err := <-served:
		log.Printf("panel: %v", err)
		code = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	mgr.Shutdown()
	return code
}
