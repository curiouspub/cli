// Package proxytest is this module's ONE place that configures process
// proxy environment for a test.
//
// net/http caches its environment-derived proxy configuration
// (HTTPS_PROXY/HTTP_PROXY/NO_PROXY) exactly once per process, in a
// sync.Once private to net/http, the first time anything dials through a
// Transport whose Proxy field is http.ProxyFromEnvironment — with no
// exported way to reset that cache. Two test files that each try to set
// their own proxy environment and expect it to be observed are therefore
// mutually exclusive within one test binary: whichever request happens
// first wins for the rest of the process, silently.
//
// This package owns the binary's one proxy configuration; no proxy row
// lives anywhere else in this module. internal/api's own suite is a
// direct consequence: it runs proxy-environment-free, which also retires
// the risk of a TestMain in that package's binary racing an unrelated
// test's own first request through http.ProxyFromEnvironment.
package proxytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// The two hostnames the proxy tests use, and nothing else in this
// package. Both sit under .test, the TLD RFC 2606 reserves for exactly
// this purpose — guaranteed never to resolve on the real internet — so
// nothing here can perform a real DNS lookup or a real network dial even
// by accident, whichever path (proxied or direct) a test takes.
const (
	proxyShouldUseHost    = "proxy-should-use.test"
	proxyShouldBypassHost = "proxy-should-bypass.test"
)

var (
	proxyBackend      *httptest.Server
	proxyBackendHits  int32
	proxyServer       *httptest.Server
	proxyConnectCount int32
	proxyBackendPool  *x509.CertPool
)

// TestMain exists for one reason, and it is specific to testing this
// package's own subject. See the package doc comment for the sync.Once
// mechanism; the fix is to fix the environment ONCE, here, before
// anything in this binary has made a request — so the first-ever read is
// deterministic rather than a race between test files — and to design
// the two proxy tests to need only ONE environment (see proxy_test.go):
// the same HTTPS_PROXY and NO_PROXY values serve both, because
// NO_PROXY's exclusion is evaluated per request against a fixed
// configuration, not re-parsed per call.
func TestMain(m *testing.M) {
	proxyBackend = newProxyTestBackend()
	proxyServer = newConnectProxy()

	// HTTP_PROXY is not used by anything here (every scenario below is
	// https) and is cleared FIRST, before HTTPS_PROXY/NO_PROXY are set,
	// so an ambient value from the operator's own shell cannot change
	// what any test observes.
	//
	// Windows environment variable NAMES are case-insensitive — SetEnv
	// and Unsetenv there operate on the same underlying variable
	// regardless of case — so "https_proxy" and "HTTPS_PROXY" are the
	// SAME variable on that platform, and so are "no_proxy" and
	// "NO_PROXY". Unsetting the lowercase spelling of either AFTER
	// setting the uppercase one below would delete the value just set,
	// on Windows only. Go's own httpproxy.FromEnvironment checks the
	// uppercase name first on every platform (getEnvAny), so setting
	// only the uppercase form is sufficient everywhere and there is
	// nothing to gain by also clearing its lowercase alias.
	_ = os.Unsetenv("HTTP_PROXY")
	_ = os.Unsetenv("http_proxy")

	if err := os.Setenv("HTTPS_PROXY", proxyServer.URL); err != nil {
		fmt.Fprintln(os.Stderr, "setenv HTTPS_PROXY:", err)
		os.Exit(1)
	}
	if err := os.Setenv("NO_PROXY", proxyShouldBypassHost); err != nil {
		fmt.Fprintln(os.Stderr, "setenv NO_PROXY:", err)
		os.Exit(1)
	}

	code := m.Run()

	proxyServer.Close()
	proxyBackend.Close()
	os.Exit(code)
}

// newProxyTestBackend is the real server both proxy tests ultimately
// talk to. Its certificate names BOTH test hostnames, because the same
// backend answers whichever route reaches it — tunnelled through the
// proxy, or dialled directly once NO_PROXY excludes it.
func newProxyTestBackend() *httptest.Server {
	cert := generateSelfSignedCert(proxyShouldUseHost, proxyShouldBypassHost)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&proxyBackendHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"open":true,"accounts_left":1,"resets_at":"2026-01-01T00:00:00Z"}`))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()

	proxyBackendPool = x509.NewCertPool()
	proxyBackendPool.AddCert(cert.Leaf)

	return srv
}

// newConnectProxy is a minimal forward proxy: it implements only
// CONNECT, and it deliberately IGNORES the requested target, always
// tunnelling to the real backend above instead. Nothing in either proxy
// test relies on this proxy honestly forwarding to an arbitrary
// destination — only on it being the thing the client actually talked
// to, which is exactly what its own counter proves.
func newConnectProxy() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "this test proxy only implements CONNECT", http.StatusMethodNotAllowed)
			return
		}
		atomic.AddInt32(&proxyConnectCount, 1)

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		clientConn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		backendConn, err := net.Dial("tcp", proxyBackend.Listener.Addr().String())
		if err != nil {
			_ = clientConn.Close()
			return
		}
		_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(backendConn, clientConn)
			close(done)
		}()
		_, _ = io.Copy(clientConn, backendConn)
		<-done
		_ = backendConn.Close()
		_ = clientConn.Close()
	}))
}

// generateSelfSignedCert makes a throwaway, short-lived certificate
// naming hosts, so the proxy tests can have their client verify the
// backend's identity for real rather than disabling verification.
func generateSelfSignedCert(hosts ...string) tls.Certificate {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: hosts[0]},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              hosts,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		panic(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}
}
