package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
	"code-contract/pkg/rpcmon"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:31806", "CSG dlink control-plane listen address (or endpoint in --query mode)")
	monitorAddr := flag.String("monitor", "127.0.0.1:31901", "RPCMonitorService CrosTCP listen address (empty to disable)")
	queryMode := flag.Bool("query", false, "query running CSG via RPCMonitorService and print JSON report")
	op := flag.String("op", "all", "query operation: port-info|server-num|server-detail|client-num|client-detail|all")
	timeout := flag.Duration("timeout", 5*time.Second, "RPCMonitorService query timeout")
	flag.Parse()

	if *queryMode {
		runQuery(*addr, *monitorAddr, *op, *timeout)
		return
	}

	srv := csgmock.NewMockCSGServer(*addr)
	srv.SetEventHook(func(event, detail string) {
		log.Printf("[csgserver] %-10s %s", event, detail)
	})
	if err := srv.Start(); err != nil {
		log.Fatalf("[csgserver] Start dlink listener on %s failed: %v", *addr, err)
	}
	log.Printf("[csgserver] CSG dlink control plane listening on %s", srv.Addr())

	if strings.TrimSpace(*monitorAddr) != "" {
		if err := srv.StartMonitor(*monitorAddr); err != nil {
			_ = srv.Close()
			log.Fatalf("[csgserver] StartMonitor on %s failed: %v", *monitorAddr, err)
		}
		log.Printf("[csgserver] RPCMonitorService (CrosTCP) listening on %s", srv.MonitorAddr())
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[csgserver] Shutting down...")
	if err := srv.Close(); err != nil {
		log.Printf("[csgserver] Close warning: %v", err)
	}
	log.Printf("[csgserver] Stopped gracefully.")
}

func runQuery(csgAddr, monitorAddr, op string, timeout time.Duration) {
	targetMon := strings.TrimSpace(monitorAddr)
	if targetMon == "" {
		resolved, err := csgdlink.ResolveServiceTCP(csgAddr, rpcmon.ServiceName, timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "csgserver query: resolve monitor from %s failed: %v\n", csgAddr, err)
			os.Exit(1)
		}
		targetMon = resolved
	}

	backend, err := rpcmon.NewCrosTCP(targetMon, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csgserver query: dial monitor %s failed: %v\n", targetMon, err)
		os.Exit(1)
	}
	defer func() { _ = backend.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	report, err := rpcmon.Fetch(ctx, backend, op)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csgserver query: fetch %s failed: %v\n", op, err)
		os.Exit(1)
	}
	out, err := report.JSON()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csgserver query: encode json failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
