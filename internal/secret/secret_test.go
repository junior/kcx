package secret

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBroker is a script with the four verbs over a cache file, the way the work
// broker behaves: get prints the cached value without a newline and fails when
// there is none; refresh reads a line from stdin and caches it; forget clears.
const fakeBroker = `#!/bin/sh
cache="$FAKE_CACHE"
case "$1" in
  get) [ -t 1 ] && { echo "refusing a terminal" >&2; exit 1; }
       [ -s "$cache" ] || { echo "nothing cached" >&2; exit 1; }
       printf '%s' "$(cat "$cache")" ;;
  refresh) IFS= read -r pw; [ -n "$pw" ] || exit 1; printf '%s' "$pw" > "$cache"; echo "cached" >&2 ;;
  forget) rm -f "$cache" ;;
  status) if [ -s "$cache" ]; then echo "cached, 22h left"; exit 0; else echo empty; exit 1; fi ;;
  *) exit 2 ;;
esac
`

func writeScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecProvider(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "kcx-secret-fake", fakeBroker)
	cache := filepath.Join(dir, "cache")
	t.Setenv("FAKE_CACHE", cache)
	lookPath := func(n string) (string, error) {
		if n == "kcx-secret-fake" {
			return path, nil
		}
		return "", os.ErrNotExist
	}
	p, err := Resolve("fake", lookPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "fake" || !p.Caches() {
		t.Fatalf("resolved: %+v", p)
	}
	ctx := context.Background()
	var errb bytes.Buffer
	p.(*Exec).Stderr = &errb
	if s, err := p.Status(ctx); err != nil || s != "empty" {
		t.Fatalf("status empty: %q %v", s, err)
	}
	r, wait, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(r)
	_ = r.Close()
	if err := wait(); err == nil || !strings.Contains(err.Error(), "get: exit 1") {
		t.Fatalf("get with nothing cached must fail: %v", err)
	}
	if err := p.Refresh(ctx, strings.NewReader("s3cret\n"), io.Discard, &errb); err != nil {
		t.Fatal(err)
	}
	if s, err := p.Status(ctx); err != nil || s != "cached, 22h left" {
		t.Fatalf("status cached: %q %v", s, err)
	}
	r, wait, err = p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if string(got) != "s3cret" {
		t.Fatalf("get: %q", got)
	}
	if err := p.Refresh(ctx, strings.NewReader("\n"), io.Discard, &errb); err == nil {
		t.Fatal("refresh with an empty line must fail")
	}
	if err := p.Forget(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.Status(ctx); s != "empty" {
		t.Fatalf("status after forget: %q", s)
	}
	if !strings.Contains(errb.String(), "nothing cached") {
		t.Fatalf("the provider's stderr must reach the user: %q", errb.String())
	}
}

func TestResolve(t *testing.T) {
	none := func(string) (string, error) { return "", os.ErrNotExist }
	for _, n := range []string{"", "none"} {
		p, err := Resolve(n, none, nil)
		if err != nil || p != nil {
			t.Fatalf("Resolve(%q) = %v %v", n, p, err)
		}
	}
	if _, err := Resolve("ghost", none, nil); err == nil || !strings.Contains(err.Error(), "neither kcx-secret-ghost nor ghost is on PATH") {
		t.Fatalf("missing provider: %v", err)
	}
	if _, err := Resolve("/no/such/program", none, nil); err == nil {
		t.Fatal("missing absolute path must fail")
	}
	// The plain name is tried after the prefixed one, which is how the work broker is found.
	calls := []string{}
	lp := func(n string) (string, error) {
		calls = append(calls, n)
		if n == "broker" {
			return "/usr/local/bin/broker", nil
		}
		return "", os.ErrNotExist
	}
	p, err := Resolve("broker", lp, nil)
	if err != nil || p.(*Exec).path != "/usr/local/bin/broker" || strings.Join(calls, ",") != "kcx-secret-broker,broker" {
		t.Fatalf("lookup order: %v %v %v", p, err, calls)
	}
	if _, err := Resolve("env:", none, nil); err == nil {
		t.Fatal("env: without a name must fail")
	}
	e, err := Resolve("env:MY_TOKEN", none, nil)
	if err != nil || e.Name() != "env:MY_TOKEN" || e.Caches() {
		t.Fatalf("env provider: %v %v", e, err)
	}
}

func TestEnvAndPromptProviders(t *testing.T) {
	ctx := context.Background()
	e := &Env{Var: "MY_TOKEN", Getenv: func(k string) string {
		if k == "MY_TOKEN" {
			return "tok"
		}
		return ""
	}}
	r, wait, err := e.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	if string(got) != "tok" || wait() != nil {
		t.Fatalf("env get: %q", got)
	}
	if s, _ := e.Status(ctx); !strings.HasPrefix(s, "cached") {
		t.Fatalf("env status: %q", s)
	}
	empty := &Env{Var: "NOPE", Getenv: func(string) string { return "" }}
	if _, _, err := empty.Get(ctx); err == nil {
		t.Fatal("empty variable must fail")
	}
	if err := empty.Refresh(ctx, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("env cannot be refreshed")
	}
	// The prompt provider reads one line from the terminal it is given, without
	// the bracketed-paste markers, and caches nothing.
	var errb bytes.Buffer
	p := &Prompt{tty: strings.NewReader("\x1b[200~pasted-secret\x1b[201~\n"), Stderr: &errb}
	r, wait, err = p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(r)
	if string(got) != "pasted-secret" || wait() != nil {
		t.Fatalf("prompt get: %q", got)
	}
	if !strings.Contains(errb.String(), "Password: ") {
		t.Fatalf("no prompt written: %q", errb.String())
	}
	if p.Caches() || p.Forget(ctx) != nil || p.Refresh(ctx, nil, io.Discard, io.Discard) != nil {
		t.Fatal("prompt caches nothing and refresh/forget are no-ops")
	}
	if s, _ := p.Status(ctx); !strings.HasPrefix(s, "empty") {
		t.Fatalf("prompt status: %q", s)
	}
	eof := &Prompt{tty: strings.NewReader(""), Stderr: io.Discard}
	if _, _, err := eof.Get(ctx); err == nil || !strings.Contains(err.Error(), "no secret entered") {
		t.Fatalf("empty prompt: %v", err)
	}
}
