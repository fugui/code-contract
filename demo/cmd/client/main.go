package main

import (
	"flag"
	"fmt"
)

func main() {
	targetAddr := flag.String("addr", "127.0.0.1:9001", "Fallback static TCP target address of SUT (used when --csg is empty)")
	csgAddr := flag.String("csg", "127.0.0.1:31806", "CSG dlink control-plane address (empty to dial --addr directly)")
	serviceName := flag.String("service", "demo.order.OrderService", "CROS RPC service name of SUT Order Gateway")
	flag.Parse()

	if *csgAddr != "" {
		fmt.Printf("Starting CROS RPC Benchmark Client via CSG %s (service=%s)...\n", *csgAddr, *serviceName)
	} else {
		fmt.Printf("Starting CROS RPC Benchmark Client connected to %s...\n", *targetAddr)
	}
	client := NewTUIClientWithCSG(*csgAddr, *serviceName, *targetAddr)
	client.Run()
}
