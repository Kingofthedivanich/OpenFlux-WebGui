package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	godebug "runtime/debug"
	"strconv"
	"strings"
	"time"
	"syscall"

	"universal-bypass-tool/socks5"
	"universal-bypass-tool/transport"
	"universal-bypass-tool/transport/cupsonline"
	"universal-bypass-tool/transport/oneme"
	"universal-bypass-tool/transport/yandex"
	"universal-bypass-tool/tunnel"
	"universal-bypass-tool/utils"
)

var (
	globalDocUrl string
	maxToken     string
	maxUid       string
	localIP      string
)

func main() {
	fmt.Print("written by p1neappleXpress\n")

	exitNode := flag.Bool("exit-node", false, "Run as exit node")
	client := flag.Bool("client", false, "Run as client")
	debug := flag.Bool("debug", false, "Enable verbose debug logging")
	socksAddr := flag.String("socks5", ":1080", "SOCKS5 address")
	transportType := flag.String("transport", "yandex", "Transport type (yandex, vyandex, oneme, cupsonline)")
	mode := flag.String("mode", "proxy", "Exit-node mode: proxy (default, works everywhere) or raw (Linux only, needs root)")
	tunMode := flag.Bool("tun", false, "Run as client using utun (macOS only, needs sudo)")
	socks5Mode := flag.Bool("socks5-mode", false, "Run as client using the legacy SOCKS5+gVisor path (slow; only for fallback/testing)")
	flag.StringVar(&globalDocUrl, "url", "http://#", "Document URL. If u use Yandex.Docs transport")
	flag.StringVar(&maxToken, "maxToken", "", "MAX Web token. If u use MAX transport")
	flag.StringVar(&maxUid, "maxUid", "", "MAX call user id. If u use MAX transport")
	flag.StringVar(&localIP, "local-ip", "", "Egress IP for exit node (raw mode only, scoped RST drop)")
	encryptionKeyFile := flag.String("encryption-key-file", "",
		"Optional: encrypt the transport with AES-256-GCM using a shared secret read from this file. "+
			"Both peers must use the same secret; unset means unencrypted, unchanged behavior")
	flag.Parse()

	exitMode, err := tunnel.ParseExitMode(*mode)
	if err != nil {
		log.Fatalf("--mode: %v", err)
	}
	// Exit node MUST be l3. Everything else (proxy/raw) uses gVisor and is
	// kept only as a client-side fallback for platforms where l3 is not
	// available. Never silently fall back on the exit.
	if *exitNode && exitMode != tunnel.ExitModeL3 {
		log.Fatalf("--exit-node requires --mode l3 (got %q). "+
			"proxy/raw are client fallbacks only.", exitMode.String())
	}

	if *exitNode && exitMode == tunnel.ExitModeRaw && localIP != "" {
		tunnel.SetLocalIP(localIP)
	}

	// The exit node often runs on a tiny VPS; keep the heap tight under load
	// (GC aggressively). Set GOMEMLIMIT in the environment for a hard soft-cap.
	if *exitNode {
		godebug.SetGCPercent(20)
	}

	if !*exitNode && !*client {
		flag.Usage()
		os.Exit(1)
	}

	if *debug {
		utils.EnableDebug()
	}

	log.Printf("=== Universal Bypass Tool ===")
	log.Printf("Mode: %s", map[bool]string{true: "EXIT NODE", false: "CLIENT"}[*exitNode])
	log.Printf("Transport: %s", *transportType)
	if *exitNode {
		log.Printf("Exit mode: %s", exitMode.String())
	}

	config := transport.DefaultConfig()
	var inner transport.Transport

	switch *transportType {
	case "vyandex":
		inner = yandex.NewYandexVolgaTransport(globalDocUrl, config)
	case "yandex":
		inner = yandex.NewYandexDocsTransport(globalDocUrl, config)
	case "oneme":
		uidint, _ := strconv.ParseInt(maxUid, 10, 64)
		inner = oneme.NewOneMeTransport(*exitNode, maxToken, uidint, config)
	case "cupsonline":
		inner = cupsonline.NewCupsonlineTransport(globalDocUrl, config, !*exitNode)
	default:
		log.Fatalf("Unknown transport type: %s", *transportType)
	}

	if *encryptionKeyFile != "" {
		secretBytes, err := os.ReadFile(*encryptionKeyFile)
		if err != nil {
			log.Fatalf("Read encryption key file: %v", err)
		}
		context := *transportType
		if globalDocUrl != "" {
			context = globalDocUrl
		}
		encrypted, err := transport.NewEncryptedTransport(inner, strings.TrimSpace(string(secretBytes)), context, *exitNode)
		if err != nil {
			log.Fatalf("Configure encrypted transport: %v", err)
		}
		inner = encrypted
		log.Printf("Transport encryption: AES-256-GCM enabled")
	}

	trans := transport.NewCompressedTransport(inner)

	if err := trans.Start(); err != nil {
		log.Fatalf("Failed to start transport: %v", err)
	}

	if *exitNode {
		ex, err := tunnel.NewExitNode(trans, "l3")
		if err != nil {
			log.Fatalf("exit node (l3): %v", err)
		}
		if ex.Mode() != "l3" {
			log.Fatalf("exit node returned mode %q, want l3", ex.Mode())
		}
		log.Printf("Running as EXIT NODE (mode=%s)", ex.Mode())
		if err := ex.Start(); err != nil {
			log.Fatalf("exit start: %v", err)
		}
		select {}
	}

	if *tunMode {
		tc, err := NewTUNClient(trans, 1280)
		if err != nil {
			log.Fatalf("utun: %v", err)
		}
		log.Printf("utun interface: %s", tc.Name())

		// Save the CURRENT default (which may be another VPN's utun) so
		// we can restore it on exit no matter what.
		if err := tc.SaveDefault(); err != nil {
			log.Fatalf("save default route: %v", err)
		}

		if err := tc.SetupInterface(); err != nil {
			log.Fatalf("setup utun (need sudo): %v", err)
		}
		log.Printf("utun up; bypass gateway is %s", tc.Gateway())

		watcher := NewSocketWatcher(tc.Gateway(), func() {
			log.Printf("Socket set stable; taking default route into the tunnel")
			if err := tc.ConfigureDefault(); err != nil {
				log.Printf("configure default: %v", err)
				return
			}
			tc.Start()
			log.Printf("Tunnel active")
		})
		watcher.Start(2 * time.Second)

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		watcher.Stop()
		log.Printf("Shutting down, restoring default route...")
		if err := tc.Close(); err != nil {
			log.Printf("cleanup warning: %v", err)
		}
		tc.RestoreDefault()
		log.Printf("Shutdown complete")
		os.Exit(0)
	}

	if !*socks5Mode {
		log.Fatalf("client mode: specify --tun (recommended, macOS L3) "+
			"or --socks5-mode (legacy gVisor fallback, slow). "+
			"Refusing to silently start the slow path.")
	}

	// Explicit opt-in to the legacy SOCKS5+gVisor client. Kept as a fallback
	// for platforms without a tun client (see README).
	log.Printf("Running as CLIENT (SOCKS5 on %s, legacy gVisor path)", *socksAddr)
	tun := tunnel.NewTCPTunnelMode(trans, false, tunnel.ExitModeProxy)
	socks5Server := socks5.NewSOCKS5Server(*socksAddr, tun)
	log.Fatal(socks5Server.Start())
}
