// Command dmeshd runs the dmesh coordinator: a dumb, non-custodial
// coordinator. It stores no user funds and signs no payments; all money
// movement is enforced by the Soroban settlement contract.
package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dmesh/internal/agent"
	"dmesh/internal/api"
	"dmesh/internal/auth"
	"dmesh/internal/config"
	"dmesh/internal/metering"
	"dmesh/internal/soroban"
	"dmesh/internal/store"
)

func main() {
	cfg := config.Load()

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()
	log.Printf("store ready at %s", cfg.DBPath)

	// Geography/tier classification runs in the node client against the
	// CI-published geo database; the coordinator stores only the signed claim.
	sorobanClient := soroban.New(cfg.SorobanRPCURL, cfg.NetworkPassphrase)
	defer sorobanClient.Close()
	var settlement *soroban.Settlement
	if cfg.SettlementContractID != "" {
		settlement = soroban.NewSettlement(sorobanClient, cfg.SettlementContractID)
		log.Printf("settlement contract bound: %s", cfg.SettlementContractID)
	} else {
		log.Printf("warning: SETTLEMENT_CONTRACT_ID not set; on-chain features are disabled")
	}

	meter := metering.New(st, cfg.MaxBandwidthBps)
	agentService := agent.New(st, meter)

	sender := auth.Sender(auth.ConsoleSender{})
	if cfg.SMTPHost != "" {
		sender = auth.SMTPSender{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			User: cfg.SMTPUser, Pass: cfg.SMTPPass, From: cfg.MailFrom,
		}
		log.Printf("dashboard OTP email via SMTP %s:%d", cfg.SMTPHost, cfg.SMTPPort)
	} else {
		log.Printf("warning: SMTP not configured; dashboard OTP codes are printed to the console (dev only)")
	}

	handler := api.New(cfg, st, sorobanClient, settlement, meter, agentService, sender)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		log.Printf("dmesh coordinator listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down")
}
