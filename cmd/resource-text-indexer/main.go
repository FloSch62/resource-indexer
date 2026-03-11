package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"resource-list/internal/config"
	"resource-list/internal/server"
	"resource-list/internal/store"
	"resource-list/internal/watcher"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Fatalf("load in-cluster config: %v", err)
	}

	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		logger.Fatalf("create dynamic client: %v", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		logger.Fatalf("create discovery client: %v", err)
	}

	state := store.New()
	mgr := watcher.New(
		logger,
		dyn,
		disc,
		state,
		cfg.ExcludeGVRs,
		cfg.ExcludeNS,
		cfg.ResyncPeriod,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		if err := mgr.Start(ctx); err != nil {
			logger.Fatalf("watch manager exited: %v", err)
		}
	}()

	svc := server.New(logger, state, mgr.Ready, cfg.MaxPageSize)
	if err := svc.Run(ctx, fmt.Sprintf(":%s", cfg.Port)); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("http server exited: %v", err)
	}
}
