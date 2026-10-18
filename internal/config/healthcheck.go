package config

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// HealthcheckIfRequested implements the container healthcheck without a
// shell in the image: when the binary is started with "-healthcheck", it
// requests path on the local listen address and exits 0 on HTTP 200, else 1.
func HealthcheckIfRequested(listenAddr, path string) {
	if len(os.Args) < 2 || os.Args[1] != "-healthcheck" {
		return
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		os.Exit(1)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		os.Exit(1)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}
