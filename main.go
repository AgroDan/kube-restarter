package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/AgroDan/kube-restarter/pkg/controller"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	interval := 21600
	if v := os.Getenv("CHECK_INTERVAL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			log.Fatalf("invalid CHECK_INTERVAL %q: %v", v, err)
		}
		interval = n
	}

	namespace := os.Getenv("NAMESPACE") // empty = all namespaces

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("failed to get in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create kubernetes client: %v", err)
	}

	log.Printf("kube-restarter started (interval=%ds, namespace=%q)", interval, namespace)

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	// Run immediately on startup, then on each tick.
	for {
		ctx := context.Background()
		if err := controller.Reconcile(ctx, clientset, namespace); err != nil {
			log.Printf("reconcile error: %v", err)
		}
		<-ticker.C
	}
}
