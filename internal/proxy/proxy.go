// Package proxy is the read-only shell's guard: a local reverse proxy in front
// of the API server that refuses, before anything leaves the machine, every
// request that could change the cluster or open a session into it. It runs on
// client-go's transport, so exec plugins, OIDC refresh and certificates are
// handled by the same code kubectl uses, and no credential copy is written
// anywhere: the shell behind it gets a kubeconfig that holds none of the
// cluster's, only a token for the proxy itself.
//
// Who may use it: a port on 127.0.0.1 is open to every user of the machine, and
// to a web page that rebinds a name of its own to 127.0.0.1. So the proxy
// answers only a request addressed to it (Host 127.0.0.1:PORT or
// localhost:PORT) that carries the session's token, a random value kept in this
// process and in the shell's kubeconfig, a file only the user can read. The
// token stops at the proxy: the API server sees the user's own credentials.
// kubectl sends credentials only to a server it reaches over TLS, so the proxy
// speaks TLS with a certificate made for the session by a CA made for the
// session, whose keys never leave this process; the shell's kubeconfig trusts
// that CA and nothing else.
// A request that impersonates (kubectl --as, --as-group, --as-uid) is refused
// too, since through the proxy it would act as someone else.
//
// The policy for the requests it answers, in order:
//
//  1. a request carrying an Upgrade header, or Connection: Upgrade, is refused:
//     exec, attach, port-forward and cp start as a GET that upgrades to a
//     WebSocket (kubectl 1.31 and later; SPDY before), so a method filter alone
//     misses them; the exec, attach and portforward paths are refused as well;
//  2. the proxy subresource of pods, services and nodes is refused: a plain GET
//     through a node proxy reaches the kubelet API, exec included;
//  3. GET, HEAD and OPTIONS pass: list, get, watch, logs, top;
//  4. the authorization and authentication review POSTs pass: they write
//     nothing, and they are what kubectl auth can-i and auth whoami send;
//  5. everything else is refused with a 405 and a reason naming kcx.
//
// Accident-proof, not adversary-proof: anything running as the user can still
// read the real kubeconfig files and bypass the proxy with --kubeconfig.
package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/junior/kcx/v2/internal/layout"
)

// Check decides whether a request may pass. It returns the reason when not.
func Check(r *http.Request) (reason string, allowed bool) {
	if isUpgrade(r) {
		return "exec, attach, port-forward and cp open a session into the cluster", false
	}
	if sub := streamSubresource(r.URL.Path); sub != "" {
		return sub + " opens a session into the cluster", false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return "", true
	case http.MethodPost:
		if isReview(r.URL.Path) {
			return "", true
		}
	}
	return r.Method + " requests change the cluster", false
}

func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, v := range r.Header.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

// streamSubresource names the session a path would open ("pod exec", "node
// proxy") or returns "" when the path only reads. Anchoring on "namespaces" (and
// on "v1" for nodes, which have none) keeps an object that happens to be named
// exec or proxy readable.
func streamSubresource(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	n := len(segs)
	if n >= 5 && segs[n-5] == "namespaces" && segs[n-3] == "pods" {
		switch segs[n-1] {
		case "exec", "attach", "portforward":
			return "pod " + segs[n-1]
		}
	}
	for i := 3; i < n; i++ {
		if segs[i] != "proxy" {
			continue
		}
		kind := segs[i-2]
		switch {
		case (kind == "pods" || kind == "services") && i >= 4 && segs[i-4] == "namespaces":
			return strings.TrimSuffix(kind, "s") + " proxy"
		case kind == "nodes" && segs[i-3] == "v1":
			return "node proxy"
		}
	}
	return ""
}

// isReview says whether path is one of the review resources that write nothing:
// SelfSubjectAccessReview, SubjectAccessReview, LocalSubjectAccessReview and
// SelfSubjectRulesReview (authorization.k8s.io), TokenReview and
// SelfSubjectReview (authentication.k8s.io), any version of their groups.
func isReview(path string) bool {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	// /apis/GROUP/VERSION/RESOURCE or /apis/GROUP/VERSION/namespaces/NS/RESOURCE
	if len(segs) < 4 || segs[0] != "apis" {
		return false
	}
	group, resource := segs[1], segs[len(segs)-1]
	switch group {
	case "authorization.k8s.io":
		switch resource {
		case "selfsubjectaccessreviews", "subjectaccessreviews", "selfsubjectrulesreviews":
			return len(segs) == 4
		case "localsubjectaccessreviews":
			return len(segs) == 6 && segs[3] == "namespaces"
		}
	case "authentication.k8s.io":
		switch resource {
		case "tokenreviews", "selfsubjectreviews":
			return len(segs) == 4
		}
	}
	return false
}

// Server is a running read-only proxy for one context.
type Server struct {
	Context  string
	token    string
	ca       []byte // the session's CA certificate, PEM
	listener net.Listener
	srv      *http.Server
}

// Start builds the transport for context name from files and serves TLS on
// 127.0.0.1 on a free port, for requests that carry a token of its own.
func Start(files []string, name string) (*Server, error) {
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: files}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: name})
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("cannot build a client for %s: %w", name, err)
	}
	target, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("the server address of %s is not a URL: %w", name, err)
	}
	if target.Scheme == "" {
		target.Scheme = "https"
	}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("cannot build the transport for %s: %w", name, err)
	}
	cert, ca, err := certificates()
	if err != nil {
		return nil, fmt.Errorf("cannot make the read-only proxy's certificate: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := rand.Text()
	s := &Server{Context: name, token: token, ca: ca, listener: ln, srv: &http.Server{
		Handler:           Handler(target, transport, ln.Addr().String(), token),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 30 * time.Second,
		// A failed handshake (a port scan, a plain http request) is not news for
		// the shell's terminal.
		ErrorLog: log.New(io.Discard, "", 0),
	}}
	go func() { _ = s.srv.ServeTLS(ln, "", "") }()
	return s, nil
}

// certificates makes the proxy's TLS identity: a CA, and a certificate for
// 127.0.0.1 and localhost that it signs, with the CA's certificate in PEM for
// the shell's kubeconfig. Both keys stay in this process.
func certificates() (tls.Certificate, []byte, error) {
	now := time.Now()
	serial := func() *big.Int {
		n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
		return n
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "kcx read-only proxy CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "kcx read-only proxy"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}

// Addr is where the proxy listens, host:port.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Close stops the proxy.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// Handler is the guarded reverse proxy for target over transport. It answers
// only requests addressed to addr, the host:port it listens on, that carry
// token as their bearer token.
func Handler(target *url.URL, transport http.RoundTripper, addr, token string) http.Handler {
	rp := &httputil.ReverseProxy{
		// SetURL sends the request to the API server's scheme, host and base path,
		// and rewrites the Host header to match, which TLS needs. The session's
		// token goes no further: client-go's transport adds the user's own
		// credentials only to a request that carries none.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.Header.Del("Authorization")
			pr.SetURL(target)
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeStatus(w, http.StatusBadGateway, metav1.StatusReasonServiceUnavailable, "[kcx] read-only shell: the API server did not answer: "+err.Error())
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !forProxy(r.Host, addr) {
			writeText(w, http.StatusForbidden, "[kcx] read-only shell: a request addressed to another host; this proxy answers only 127.0.0.1 and localhost")
			return
		}
		if !hasToken(r, token) {
			writeText(w, http.StatusUnauthorized, "[kcx] read-only shell: a request without the shell's token; only the read-only shell's own kubeconfig can use this proxy")
			return
		}
		if impersonates(r) {
			writeText(w, http.StatusForbidden, "[kcx] read-only shell: impersonation (--as, --as-group, --as-uid) would act as someone else with your credentials; refused before leaving the machine")
			return
		}
		if reason, ok := Check(r); !ok {
			writeStatus(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "[kcx] read-only shell: "+reason+"; refused before leaving the machine")
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// forProxy says whether host, a request's Host, names the proxy itself: its
// port, on 127.0.0.1 or localhost. A page that rebinds a name of its own to
// 127.0.0.1 still sends that name.
func forProxy(host, addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != port {
		return false
	}
	return h == "127.0.0.1" || strings.EqualFold(h, "localhost")
}

// hasToken says whether r carries token as its bearer token, compared in
// constant time.
func hasToken(r *http.Request, token string) bool {
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(value)), []byte(token)) == 1
}

// impersonates says whether r asks to act as another user, group or uid.
func impersonates(r *http.Request) bool {
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "impersonate-") {
			return true
		}
	}
	return false
}

// writeText answers in plain text, which kubectl shows as the error's message
// even on its first discovery request, where it cannot decode a Status: a
// request turned away before the policy applies fails there first.
func writeText(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, message+"\n")
}

// writeStatus answers with a Kubernetes Status object, which kubectl renders as
// "Error from server (Reason): message".
func writeStatus(w http.ResponseWriter, code int, reason metav1.StatusReason, message string) {
	status := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
		Message:  message,
		Reason:   reason,
		Code:     int32(code),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}

// ReadonlyContext is the name the read-only kubeconfig gives the context, so
// prompts and tools show the mode at a glance.
func ReadonlyContext(name string) string { return name + " (READ-ONLY)" }

// WriteKubeconfig writes the kubeconfig the read-only shell uses: one cluster
// pointing at the proxy and trusting the session's CA, a user holding the
// session's token and none of the cluster's credentials, the context renamed.
// Mode 0600, so that no one else can read the token; a namespace change inside
// the shell (kcx ns, or kubectl config) lands in this file and nowhere else.
func (s *Server) WriteKubeconfig(path string) error {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["readonly"] = &clientcmdapi.Cluster{Server: "https://" + s.Addr(), CertificateAuthorityData: s.ca}
	cfg.AuthInfos["readonly"] = &clientcmdapi.AuthInfo{Token: s.token}
	ro := ReadonlyContext(s.Context)
	cfg.Contexts[ro] = &clientcmdapi.Context{Cluster: "readonly", AuthInfo: "readonly"}
	cfg.CurrentContext = ro
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return err
	}
	return layout.WriteFileAtomic(path, data, 0o600)
}
