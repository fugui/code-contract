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
	addr := flag.String("addr", "127.0.0.1:9001", "TCP listen address for SUT Order Center")
	csgAddr := flag.String("csg", "127.0.0.1:31806", "CSG dlink control-plane address (empty to use static downstream addresses)")
	serviceName := flag.String("service-name", "demo.order.OrderService", "Published CROS RPC service name for SUT Order Gateway")
	accountAddr := flag.String("account-addr", "127.0.0.1:9002", "Fallback static address of Backend Account Service (used when --csg is empty)")
	inventoryAddr := flag.String("inventory-addr", "127.0.0.1:9003", "Fallback static address of Backend Inventory Service (used when --csg is empty)")
	payAlipayAddr := flag.String("payment-alipay-addr", "127.0.0.1:9004", "Fallback static address of Payment Channel Instance (ALIPAY)")
	payAlipaySvc := flag.String("payment-alipay-service", "demo.payment.ALIPAY_PaymentService", "Published CROS service name for ALIPAY payment instance")
	payWechatAddr := flag.String("payment-wechat-addr", "127.0.0.1:9005", "Fallback static address of Payment Channel Instance (WECHAT)")
	payWechatSvc := flag.String("payment-wechat-service", "demo.payment.WECHAT_PaymentService", "Published CROS service name for WECHAT payment instance")
	flag.Parse()

	log.Printf("[sut-order] Initializing SUT CROS RPC Order Gateway on %s (service=%s, csg=%q)...", *addr, *serviceName, *csgAddr)
	if *csgAddr == "" {
		log.Printf("[sut-order] Static Downstream Account Service: %s, Inventory Service: %s", *accountAddr, *inventoryAddr)
		log.Printf("[sut-order] Static Payment Dispatch Routes: ALIPAY=%s (%s), WECHAT=%s (%s)",
			*payAlipayAddr, *payAlipaySvc, *payWechatAddr, *payWechatSvc)
	}

	orchestrator := NewOrderOrchestratorWithCSG(*csgAddr, *accountAddr, *inventoryAddr, map[string]PaymentRoute{
		"ALIPAY": {Addr: *payAlipayAddr, ServiceName: *payAlipaySvc},
		"WECHAT": {Addr: *payWechatAddr, ServiceName: *payWechatSvc},
	})
	server := crosframe.NewServer(*addr, "sut-order")
	server.SetCSGAddr(*csgAddr)
	orchestrator.RegisterHandlersForService(server, *serviceName)

	if err := server.Start(); err != nil {
		log.Fatalf("[sut-order] Start failed: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[sut-order] Shutting down...")
	_ = server.Stop()
	orchestrator.Close()
	log.Printf("[sut-order] SUT Order Gateway stopped gracefully.")
}
