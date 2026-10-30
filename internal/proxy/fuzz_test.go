package proxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// FuzzCheck: whatever the method, path and headers, Check lets through only
// reads (GET, HEAD, OPTIONS) on a clean path that open no session, and the
// review POSTs of the authorization and authentication groups; it never
// panics, and it gives a reason for every refusal. `go test` runs the seeds;
// `go test -fuzz FuzzCheck ./internal/proxy` searches further.
func FuzzCheck(f *testing.F) {
	for _, s := range [][4]string{
		{"GET", "/api/v1/namespaces/default/pods", "", ""},
		{"HEAD", "/healthz", "", ""},
		{"POST", "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", "", ""},
		{"POST", "/apis/authorization.k8s.io/v1/namespaces/default/localsubjectaccessreviews", "", ""},
		{"POST", "/api/v1/namespaces/default/pods", "", ""},
		{"GET", "/api/v1/namespaces/default/pods/web/exec", "", ""},
		{"GET", "/api/v1/nodes/n1/proxy/pods", "", ""},
		{"GET", "/api/v1/nodes/n1/./proxy/pods", "", ""},
		{"GET", "/api/v1/namespaces/default/pods/web", "websocket", "Upgrade"},
		{"GET", "/api/v1/namespaces/default/pods/web", "", "keep-alive, upgrade"},
		{"DELETE", "/api/v1/namespaces/default/pods/web", "", ""},
		{"PATCH", "/apis/authentication.k8s.io/v1/tokenreviews", "", ""},
	} {
		f.Add(s[0], s[1], s[2], s[3])
	}
	f.Fuzz(func(t *testing.T, method, path, upgrade, connection string) {
		r := &http.Request{Method: method, URL: &url.URL{Path: path}, Header: http.Header{}}
		if upgrade != "" {
			r.Header.Set("Upgrade", upgrade)
		}
		if connection != "" {
			r.Header.Set("Connection", connection)
		}
		reason, ok := Check(r)
		if !ok {
			if reason == "" {
				t.Fatalf("refused without a reason: %q %q", method, path)
			}
			return
		}
		if isUpgrade(r) {
			t.Fatalf("an upgrade passed: %q %q upgrade=%q connection=%q", method, path, upgrade, connection)
		}
		if !cleanPath(path) {
			t.Fatalf("a path that is not clean passed: %q", path)
		}
		if sub := streamSubresource(path); sub != "" {
			t.Fatalf("%s passed: %q", sub, path)
		}
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		case http.MethodPost:
			segs := strings.Split(strings.Trim(path, "/"), "/")
			if len(segs) < 4 || segs[0] != "apis" ||
				(segs[1] != "authorization.k8s.io" && segs[1] != "authentication.k8s.io") ||
				!strings.HasSuffix(segs[len(segs)-1], "reviews") {
				t.Fatalf("a POST that is no review passed: %q", path)
			}
		default:
			t.Fatalf("method %q passed: %q", method, path)
		}
	})
}
