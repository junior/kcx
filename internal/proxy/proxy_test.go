package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func TestCheckTable(t *testing.T) {
	type tc struct {
		method, path string
		headers      map[string]string
		allowed      bool
	}
	cases := []tc{
		{"GET", "/api/v1/namespaces/default/pods", nil, true},
		{"GET", "/api/v1/namespaces/default/pods?watch=true", nil, true},
		{"GET", "/api/v1/namespaces/default/pods/web/log", nil, true},
		{"GET", "/apis/metrics.k8s.io/v1beta1/pods", nil, true},
		{"HEAD", "/healthz", nil, true},
		{"OPTIONS", "/api", nil, true},
		{"GET", "/version", nil, true},
		{"POST", "/api/v1/namespaces/default/pods", nil, false},
		{"PUT", "/api/v1/namespaces/default/pods/web", nil, false},
		{"PATCH", "/api/v1/namespaces/default/pods/web", nil, false},
		{"DELETE", "/api/v1/namespaces/default/pods/web", nil, false},
		{"POST", "/api/v1/namespaces/default/pods?dryRun=All", nil, false},
		{"POST", "/api/v1/namespaces/default/pods/web/exec", nil, false},
		{"GET", "/api/v1/namespaces/default/pods/web/exec?command=sh", nil, false},
		{"GET", "/api/v1/namespaces/default/pods/web/attach", nil, false},
		{"GET", "/api/v1/namespaces/default/pods/web/portforward", nil, false},
		{"GET", "/api/v1/namespaces/default/pods/web/status", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"}, false},
		{"GET", "/api/v1/namespaces/default/pods/web/status", map[string]string{"Connection": "keep-alive, Upgrade"}, false},
		{"GET", "/api/v1/namespaces/default/pods/web/proxy/healthz", nil, false},
		{"GET", "/api/v1/namespaces/default/pods/web:8080/proxy/", nil, false},
		{"GET", "/api/v1/namespaces/default/services/web/proxy/", nil, false},
		{"GET", "/api/v1/nodes/worker-1/proxy/pods", nil, false},
		{"GET", "/api/v1/nodes/worker-1/proxy/exec/default/web/app", nil, false},
		// A path that is not clean could read one way here and another way
		// behind a load balancer that cleans it; a trailing slash is fine.
		{"GET", "/api/v1/namespaces/default/pods/web/./exec", nil, false},
		{"GET", "/api/v1/nodes/worker-1/./proxy/pods", nil, false},
		{"GET", "/api/v1/namespaces/default/pods/x/../web/proxy/", nil, false},
		{"GET", "/api//v1/nodes/worker-1/proxy/pods", nil, false},
		{"GET", "/api/v1/namespaces/default/pods/", nil, true},
		{"GET", "/", nil, true},
		// Objects that happen to be called exec or proxy stay readable.
		{"GET", "/api/v1/namespaces/default/configmaps/exec", nil, true},
		{"GET", "/api/v1/namespaces/proxy/pods", nil, true},
		{"GET", "/api/v1/namespaces/default/pods/proxy", nil, true},
		// The review resources write nothing and are what auth can-i and whoami send.
		{"POST", "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", nil, true},
		{"POST", "/apis/authorization.k8s.io/v1/subjectaccessreviews", nil, true},
		{"POST", "/apis/authorization.k8s.io/v1/selfsubjectrulesreviews", nil, true},
		{"POST", "/apis/authorization.k8s.io/v1/namespaces/default/localsubjectaccessreviews", nil, true},
		{"POST", "/apis/authentication.k8s.io/v1/tokenreviews", nil, true},
		{"POST", "/apis/authentication.k8s.io/v1beta1/selfsubjectreviews", nil, true},
		// Look-alikes do not pass.
		{"POST", "/api/v1/namespaces/default/selfsubjectaccessreviews", nil, false},
		{"POST", "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews/extra", nil, false},
		{"POST", "/apis/authorization.k8s.io/v1/namespaces/default/subjectaccessreviews", nil, false},
		{"PUT", "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", nil, false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		reason, allowed := Check(req)
		if allowed != c.allowed {
			t.Errorf("%s %s %v: allowed=%v (reason %q), want %v", c.method, c.path, c.headers, allowed, reason, c.allowed)
		}
		if !allowed && reason == "" {
			t.Errorf("%s %s: refused without a reason", c.method, c.path)
		}
	}
}

func TestEndToEnd(t *testing.T) {
	var seen []string
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization")+" host="+r.Host)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"PodList","items":[]}`))
	}))
	defer backend.Close()
	home := t.TempDir()
	kc := filepath.Join(home, "kube.yaml")
	content := "apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster:\n    server: " + backend.URL +
		"\n    insecure-skip-tls-verify: true\nusers:\n- name: u\n  user:\n    token: not-a-secret\ncontexts:\n- name: prod\n  context:\n    cluster: c\n    user: u\n"
	if err := os.WriteFile(kc, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Start([]string{kc}, "prod")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := "https://" + s.Addr()
	_, port, _ := net.SplitHostPort(s.Addr())
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(s.ca) {
		t.Fatal("the session's CA is not a PEM certificate")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	// get sends the session's token unless hdr empties Authorization; a Host in
	// hdr becomes the request's Host.
	get := func(method, path string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+s.token)
		for k, v := range hdr {
			switch {
			case k == "Host":
				req.Host = v
			case v == "":
				req.Header.Del(k)
			default:
				req.Header.Set(k, v)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}
	resp, body := get("GET", "/api/v1/namespaces/default/pods", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "PodList") {
		t.Fatalf("GET through the proxy: %d %s", resp.StatusCode, body)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "auth=Bearer not-a-secret") {
		t.Fatalf("the backend must see the real credentials: %v", seen)
	}
	if strings.Contains(seen[0], s.token) {
		t.Fatalf("the session's token went past the proxy: %v", seen)
	}
	host := strings.TrimPrefix(backend.URL, "https://")
	if !strings.Contains(seen[0], "host="+host) {
		t.Fatalf("Host header not rewritten: %v", seen)
	}
	resp, body = get("POST", "/api/v1/namespaces/default/pods", nil)
	if resp.StatusCode != 405 {
		t.Fatalf("POST not refused: %d %s", resp.StatusCode, body)
	}
	var status struct {
		Kind, Status, Message, Reason string
		Code                          int
	}
	if err := json.Unmarshal([]byte(body), &status); err != nil || status.Kind != "Status" || status.Reason != "MethodNotAllowed" || !strings.Contains(status.Message, "[kcx] read-only shell") {
		t.Fatalf("refusal body: %s (%v)", body, err)
	}
	if len(seen) != 1 {
		t.Fatal("a refused request reached the backend")
	}
	resp, _ = get("GET", "/api/v1/namespaces/default/pods/web/exec", nil)
	if resp.StatusCode != 405 || len(seen) != 1 {
		t.Fatalf("exec not refused: %d", resp.StatusCode)
	}
	resp, _ = get("GET", "/api/v1/namespaces/default/pods/web", map[string]string{"Connection": "Upgrade", "Upgrade": "SPDY/3.1"})
	if resp.StatusCode != 405 || len(seen) != 1 {
		t.Fatalf("upgrade not refused: %d", resp.StatusCode)
	}
	resp, _ = get("POST", "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", nil)
	if resp.StatusCode != 200 || len(seen) != 2 {
		t.Fatalf("auth can-i refused: %d", resp.StatusCode)
	}
	resp, _ = get("POST", "/api/v1/namespaces/default/pods?dryRun=All", nil)
	if resp.StatusCode != 405 {
		t.Fatalf("dry run not refused: %d", resp.StatusCode)
	}
	// Only the shell's own kubeconfig can use the proxy: another user of the
	// machine has no token, and a page that rebinds a name to 127.0.0.1 has none
	// either and sends its own name as the Host. Impersonation is refused.
	for _, c := range []struct {
		hdr  map[string]string
		code int
	}{
		{map[string]string{"Authorization": ""}, 401},
		{map[string]string{"Authorization": "Bearer wrong"}, 401},
		{map[string]string{"Authorization": "Bearer " + s.token + "x"}, 401},
		{map[string]string{"Authorization": "Basic " + s.token}, 401},
		{map[string]string{"Host": "rebound.example:" + port}, 403},
		{map[string]string{"Host": "127.0.0.1:1"}, 403},
		{map[string]string{"Host": "127.0.0.1"}, 403},
		{map[string]string{"Impersonate-User": "admin"}, 403},
		{map[string]string{"Impersonate-Group": "system:masters"}, 403},
		{map[string]string{"Impersonate-Uid": "1"}, 403},
		{map[string]string{"Impersonate-Extra-Scopes": "x"}, 403},
	} {
		n := len(seen)
		resp, body := get("GET", "/api/v1/namespaces/default/pods", c.hdr)
		if resp.StatusCode != c.code || len(seen) != n || !strings.Contains(body, "[kcx] read-only shell") {
			t.Errorf("%v: %d %s (reached the backend: %v)", c.hdr, resp.StatusCode, body, len(seen) != n)
		}
	}
	if resp, body := get("GET", "/version", map[string]string{"Host": "LocalHost:" + port}); resp.StatusCode != 200 {
		t.Fatalf("localhost is the proxy too: %d %s", resp.StatusCode, body)
	}
	// Plain http, or a client that does not trust the session's CA, gets nowhere.
	n := len(seen)
	if resp, err := http.Get("http://" + s.Addr() + "/version"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("plain http answered")
		}
	}
	if _, err := http.Get(base + "/version"); err == nil {
		t.Fatal("a client without the session's CA must fail the handshake")
	}
	if len(seen) != n {
		t.Fatal("plain http or an untrusting client reached the backend")
	}
	// A kubeconfig for the shell: the session's token and none of the cluster's
	// credentials, the proxy's address, the renamed context.
	ro := filepath.Join(home, "readonly.yaml")
	if err := s.WriteKubeconfig(ro); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(ro)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	cfg, err := clientcmd.LoadFromFile(ro)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "prod (READ-ONLY)" || cfg.Clusters["readonly"].Server != "https://"+s.Addr() || string(cfg.Clusters["readonly"].CertificateAuthorityData) != string(s.ca) || cfg.AuthInfos["readonly"].Token != s.token || len(s.token) < 20 {
		t.Fatalf("read-only kubeconfig: %+v", cfg)
	}
	data, _ := os.ReadFile(ro)
	if strings.Contains(string(data), "not-a-secret") {
		t.Fatal("the read-only kubeconfig carries a credential")
	}
	// What kubectl does with that kubeconfig: client-go sends the token to the
	// proxy over plain http, and the backend sees the real credentials only.
	restCfg, err := clientcmd.BuildConfigFromFlags("", ro)
	if err != nil {
		t.Fatal(err)
	}
	hc, err := rest.HTTPClientFor(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	n = len(seen)
	resp, err = hc.Get(restCfg.Host + "/api/v1/namespaces/default/pods")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(seen) != n+1 || !strings.Contains(seen[n], "auth=Bearer not-a-secret") || strings.Contains(seen[n], s.token) {
		t.Fatalf("client-go through the read-only kubeconfig: %d %v", resp.StatusCode, seen[n:])
	}
	// The backend gone: a 502 Status, not a hang.
	backend.Close()
	resp, body = get("GET", "/version", nil)
	if resp.StatusCode != 502 || !strings.Contains(body, "did not answer") {
		t.Fatalf("backend gone: %d %s", resp.StatusCode, body)
	}
	if _, err := Start([]string{kc}, "missing"); err == nil {
		t.Fatal("Start with an unknown context must fail")
	}
	u, _ := url.Parse("https://example.test")
	if Handler(u, http.DefaultTransport, "127.0.0.1:1", "t") == nil {
		t.Fatal("Handler")
	}
}

// TestOIDCUser is the shape of a platform login: an oidc auth-provider whose
// id-token has not expired. client-go's provider, like the bearer token, exec
// and basic ones, adds its credentials only to a request that carries none, so
// the proxy must drop the session's token for the id-token to reach the server.
func TestOIDCUser(t *testing.T) {
	var auth []string
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"NamespaceList","items":[]}`))
	}))
	defer backend.Close()
	enc := base64.RawURLEncoding.EncodeToString
	idToken := enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"exp":4102444800,"sub":"jane"}`)) + ".c2ln"
	kc := filepath.Join(t.TempDir(), "kube.yaml")
	content := "apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster:\n    server: " + backend.URL +
		"\n    insecure-skip-tls-verify: true\nusers:\n- name: u\n  user:\n    auth-provider:\n      name: oidc\n      config:\n" +
		"        client-id: kubernetes\n        idp-issuer-url: https://issuer.invalid\n        id-token: " + idToken +
		"\n        refresh-token: not-a-secret\ncontexts:\n- name: lab\n  context:\n    cluster: c\n    user: u\n"
	if err := os.WriteFile(kc, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Start([]string{kc}, "lab")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ro := filepath.Join(t.TempDir(), "readonly.yaml")
	if err := s.WriteKubeconfig(ro); err != nil {
		t.Fatal(err)
	}
	restCfg, err := clientcmd.BuildConfigFromFlags("", ro)
	if err != nil {
		t.Fatal(err)
	}
	hc, err := rest.HTTPClientFor(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Get(restCfg.Host + "/api/v1/namespaces")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(auth) != 1 || auth[0] != "Bearer "+idToken {
		t.Fatalf("the API server must see the id-token: %d %q", resp.StatusCode, auth)
	}
}

func TestForProxyAndToken(t *testing.T) {
	for _, c := range []struct {
		host string
		want bool
	}{
		{"127.0.0.1:5000", true},
		{"localhost:5000", true},
		{"LOCALHOST:5000", true},
		{"127.0.0.1:5001", false},
		{"127.0.0.1", false},
		{"[::1]:5000", false},
		{"127.0.0.2:5000", false},
		{"localhost.rebound.example:5000", false},
		{"", false},
	} {
		if got := forProxy(c.host, "127.0.0.1:5000"); got != c.want {
			t.Errorf("forProxy(%q) = %v, want %v", c.host, got, c.want)
		}
	}
	for _, c := range []struct {
		header string
		want   bool
	}{
		{"Bearer tok", true},
		{"bearer tok", true},
		{"Bearer  tok", true},
		{"Bearer tok2", false},
		{"Bearer to", false},
		{"Bearer", false},
		{"tok", false},
		{"Basic tok", false},
		{"", false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := hasToken(r, "tok"); got != c.want {
			t.Errorf("hasToken(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}
