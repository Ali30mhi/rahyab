package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

var noProxy bool

func newClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if noProxy {
		tr.Proxy = nil
	} else if usingProxy() {
		fmt.Println("note: a proxy/VPN is set, so results show the VPN route, not the Iranian network.")
		fmt.Println("      Add --no-proxy to test the direct connection.")
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

func usingProxy() bool {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}
