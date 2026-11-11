package main

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

var noProxy bool

var proxyNote sync.Once

func newClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if noProxy {
		tr.Proxy = nil
	} else if usingProxy() {
		proxyNote.Do(func() {
			fmt.Println("note: a proxy/VPN is set, so results show the VPN route, not the Iranian network.")
			fmt.Println("      Add --no-proxy to test the direct connection.")
		})
	} else if allProxyOnly() {
		proxyNote.Do(func() {
			fmt.Println("note: ALL_PROXY is set, but rahyab only reads HTTP_PROXY/HTTPS_PROXY, so it is testing")
			fmt.Println("      the direct connection. Set HTTPS_PROXY to test through the proxy.")
		})
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// usingProxy reports whether requests really go through a proxy: Go's
// net/http reads HTTP(S)_PROXY and nothing else.
func usingProxy() bool {
	return anyEnv("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy")
}

// allProxyOnly: ALL_PROXY is a curl convention that net/http ignores, so
// with only that set the probes go out directly.
func allProxyOnly() bool {
	return !usingProxy() && anyEnv("ALL_PROXY", "all_proxy")
}

func anyEnv(keys ...string) bool {
	for _, k := range keys {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}
