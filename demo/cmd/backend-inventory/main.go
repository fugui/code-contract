package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"code-contract/demo/pkg/crosframe"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9003", "TCP listen address for Inventory Service")
	csgAddr := flag.String("csg", "127.0.0.1:31806", "CSG dlink control-plane address (empty to disable CSG registration)")
	flag.Parse()

	log.Printf("[backend-inventory] Initializing CROS RPC Inventory Service...")
	store := NewInventoryStore()
	server := crosframe.NewServer(*addr, "backend-inventory")
	server.SetCSGAddr(*csgAddr)
	store.RegisterHandlers(server)

	if err := server.Start(); err != nil {
		log.Fatalf("[backend-inventory] Start failed: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[backend-inventory] Shutting down...")
	_ = server.Stop()
	log.Printf("[backend-inventory] Stopped gracefully.")
}
