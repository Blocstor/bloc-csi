package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"

	"github.com/blocstor/bloc-csi/internal/driver"
	"github.com/blocstor/bloc-csi/internal/manager"
)

func main() {
	var (
		endpoint   = flag.String("endpoint", "unix:///var/lib/kubelet/plugins/bloc.csi.blocstor/csi.sock", "CSI endpoint (unix:// or tcp://)")
		managerURL = flag.String("manager-url", "http://bloc-manager:9090", "URL of the bloc-manager REST API")
		nodeName   = flag.String("node-name", os.Getenv("NODE_NAME"), "Kubernetes node name (defaults to NODE_NAME env var)")
	)
	flag.Parse()

	log.Printf("bloc-csi driver %s starting", driver.Version)
	log.Printf("  endpoint:    %s", *endpoint)
	log.Printf("  manager-url: %s", *managerURL)
	log.Printf("  node-name:   %s", *nodeName)

	// Parse endpoint into network+address.
	network, addr, err := parseEndpoint(*endpoint)
	if err != nil {
		log.Fatalf("invalid endpoint %q: %v", *endpoint, err)
	}

	// Remove stale socket file if present.
	if network == "unix" {
		if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
			log.Fatalf("remove stale socket %s: %v", addr, err)
		}
		// Ensure parent directory exists.
		idx := strings.LastIndex(addr, "/")
		if idx > 0 {
			dir := addr[:idx]
			if err := os.MkdirAll(dir, 0750); err != nil {
				log.Fatalf("create socket dir %s: %v", dir, err)
			}
		}
	}

	lis, err := net.Listen(network, addr)
	if err != nil {
		log.Fatalf("listen on %s://%s: %v", network, addr, err)
	}

	mgr := manager.NewClient(*managerURL)

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(logInterceptor),
	)

	csi.RegisterIdentityServer(srv, &driver.IdentityServer{})
	csi.RegisterControllerServer(srv, driver.NewControllerServer(mgr, nil))
	csi.RegisterNodeServer(srv, driver.NewNodeServer(*nodeName, *managerURL))

	// Graceful shutdown on SIGINT/SIGTERM.
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stopCh
		log.Printf("received signal %v, shutting down", sig)
		srv.GracefulStop()
	}()

	log.Printf("listening on %s://%s", network, addr)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// parseEndpoint splits a CSI endpoint string into network and address.
// Supported formats: unix:///path, unix://path, tcp://host:port.
func parseEndpoint(ep string) (string, string, error) {
	if strings.HasPrefix(ep, "unix://") {
		addr := strings.TrimPrefix(ep, "unix://")
		return "unix", addr, nil
	}
	if strings.HasPrefix(ep, "tcp://") {
		addr := strings.TrimPrefix(ep, "tcp://")
		return "tcp", addr, nil
	}
	return "", "", fmt.Errorf("unsupported scheme in %q (want unix:// or tcp://)", ep)
}

// logInterceptor logs each incoming RPC call.
func logInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	log.Printf("RPC: %s", info.FullMethod)
	resp, err := handler(ctx, req)
	if err != nil {
		log.Printf("RPC %s error: %v", info.FullMethod, err)
	}
	return resp, err
}
