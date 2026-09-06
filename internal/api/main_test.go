package api

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
// file's own subject. net/http caches the parsed HTTPS_PROXY/HTTP_PROXY/
// NO_PROXY environment exactly ONCE, in a sync.Once private to net/http,
// with no exported way to reset it from outside that package — the first
// real request made through any Transport whose Proxy field is
// http.ProxyFromEnvironment freezes it for the rest of the process. So
// the two proxy tests below cannot each set their own environment with
// t.Setenv and expect the second one's change to take effect.
//
// The fix is to fix the environment ONCE, here, before anything in this
// binary has made a request — so the first-ever read is deterministic
// rather than a race between test files — and to design the two proxy
// tests to need only ONE environment (see proxy_test.go): the same
// HTTPS_PROXY and NO_PROXY values serve both, because NO_PROXY's
// exclusion is evaluated per request against a fixed configuration, not
// re-parsed per call.
//
// Every OTHER test in this package targets a loopback listener
// (127.0.0.1, via httptest.NewServer). net/http's own proxy resolution
// exempts loopback addresses unconditionally, regardless of HTTPS_PROXY
// or NO_PROXY, so fixing these two variables for the whole process does
// not change what any non-proxy test observes.
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
	// on Windows only: exactly the failure this comment now prevents,
	// caught by the Windows leg of this repo's own CI matrix. Go's own
	// httpproxy.FromEnvironment checks the uppercase name first on every
	// platform (getEnvAny), so setting only the uppercase form is
	// sufficient everywhere and there is nothing to gain by also
	// clearing its lowercase alias.
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
