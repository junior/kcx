package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	base := "http://" + s.Addr()
	get := func(method, path string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader("{}"))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
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
	// A kubeconfig for the shell: no credentials, the proxy's address, the renamed context.
	ro := filepath.Join(home, "readonly.yaml")
	if err := WriteKubeconfig(ro, "prod", s.Addr()); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(ro)
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	cfg, err := clientcmd.LoadFromFile(ro)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "prod (READ-ONLY)" || cfg.Clusters["readonly"].Server != "http://"+s.Addr() || len(cfg.AuthInfos["readonly"].Token) != 0 {
		t.Fatalf("read-only kubeconfig: %+v", cfg)
	}
	data, _ := os.ReadFile(ro)
	if strings.Contains(string(data), "not-a-secret") {
		t.Fatal("the read-only kubeconfig carries a credential")
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
	if Handler(u, http.DefaultTransport) == nil {
		t.Fatal("Handler")
	}
}
