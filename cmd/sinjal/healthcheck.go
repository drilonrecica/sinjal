package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// healthcheck is `sinjal healthcheck` (docs/15 "CLI"): GET /healthz on the
// local listener, exit 0 on 200 and 1 otherwise. It is the Docker
// HEALTHCHECK of the scratch image, which has no curl. It reads only
// SINJAL_LISTEN, opens no database and calls nothing else.
func healthcheck(stderr io.Writer) int {
	url, err := healthURL(os.Getenv("SINJAL_LISTEN"))
	if err != nil {
		fmt.Fprintf(stderr, "sinjal healthcheck: SINJAL_LISTEN: %v\n", err)
		return 1
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(stderr, "sinjal healthcheck: %v\n", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "sinjal healthcheck: %s answered %d\n", url, resp.StatusCode)
		return 1
	}
	return 0
}

// healthURL is the /healthz address on loopback at the port of listen
// (SINJAL_LISTEN, default ":8080"), whatever host it names.
func healthURL(listen string) (string, error) {
	if listen == "" {
		listen = ":8080"
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz", nil
}
