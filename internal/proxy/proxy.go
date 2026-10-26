// Package proxy is the read-only shell's guard: a local reverse proxy in front
// of the API server that refuses, before anything leaves the machine, every
// request that could change the cluster or open a session into it. It runs on
// client-go's transport, so exec plugins, OIDC refresh and certificates are
// handled by the same code kubectl uses, and no credential copy is written
// anywhere: the shell behind it gets a kubeconfig that holds none.
//
// The policy, in order:
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
	"encoding/json"
	"fmt"
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

	"github.com/junior/kcx/internal/layout"
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
	listener net.Listener
	srv      *http.Server
}

// Start builds the transport for context name from files and serves on
// 127.0.0.1 on a free port.
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
	handler := Handler(target, transport)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{Context: name, listener: ln, srv: &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Addr is where the proxy listens, host:port.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Close stops the proxy.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// Handler is the guarded reverse proxy for target over transport.
func Handler(target *url.URL, transport http.RoundTripper) http.Handler {
	rp := &httputil.ReverseProxy{
		// SetURL sends the request to the API server's scheme, host and base path,
		// and rewrites the Host header to match, which TLS needs.
		Rewrite:   func(pr *httputil.ProxyRequest) { pr.SetURL(target) },
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeStatus(w, http.StatusBadGateway, metav1.StatusReasonServiceUnavailable, "[kcx] read-only shell: the API server did not answer: "+err.Error())
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason, ok := Check(r); !ok {
			writeStatus(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "[kcx] read-only shell: "+reason+"; refused before leaving the machine")
			return
		}
		rp.ServeHTTP(w, r)
	})
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

// WriteKubeconfig writes the kubeconfig the read-only shell uses: no credentials,
// one cluster pointing at the proxy, the context renamed. Mode 0600, so that a
// namespace change inside the shell (kcx ns, or kubectl config) lands in this
// file and nowhere else; there is nothing in it to protect.
func WriteKubeconfig(path, name, addr string) error {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["readonly"] = &clientcmdapi.Cluster{Server: "http://" + addr}
	cfg.AuthInfos["readonly"] = &clientcmdapi.AuthInfo{}
	ro := ReadonlyContext(name)
	cfg.Contexts[ro] = &clientcmdapi.Context{Cluster: "readonly", AuthInfo: "readonly"}
	cfg.CurrentContext = ro
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return err
	}
	return layout.WriteFileAtomic(path, data, 0o600)
}
