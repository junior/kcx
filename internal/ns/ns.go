// Package ns handles namespaces and the one network call kcx makes on its own
// account: a namespace is a property of a context entry, so kubectl reads it from
// the file that defines the context and kcx writes it there, exactly as kubectl
// and kubens do; the previous namespace is kept per context in the file kubens
// uses, so `kcx ns -` and `kubens -` agree.
package ns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/junior/kcx/internal/layout"
)

// Current returns the current context and its namespace ("default" when the
// context sets none, as kubectl behaves).
func Current(files []string) (ctx, namespace string, err error) {
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: files}
	cfg, err := rules.Load()
	if err != nil {
		return "", "", err
	}
	ctx = cfg.CurrentContext
	if ctx == "" {
		return "", "", errors.New("no current context (kcx NAME picks one)")
	}
	c, ok := cfg.Contexts[ctx]
	if !ok {
		return ctx, "", fmt.Errorf("the current context, %s, is not defined in any file", ctx)
	}
	namespace = c.Namespace
	if namespace == "" {
		namespace = "default"
	}
	return ctx, namespace, nil
}

// Of returns the namespace of context ctx ("default" when unset).
func Of(files []string, ctx string) (string, error) {
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: files}
	cfg, err := rules.Load()
	if err != nil {
		return "", err
	}
	c, ok := cfg.Contexts[ctx]
	if !ok {
		return "", fmt.Errorf("no context named %s", ctx)
	}
	if c.Namespace == "" {
		return "default", nil
	}
	return c.Namespace, nil
}

// Set writes namespace into context ctx, in the file that defines the context
// (clientcmd's rule for a modified entry), leaving every other file alone.
func Set(files []string, ctx, namespace string) error {
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: files}
	cfg, err := rules.Load()
	if err != nil {
		return err
	}
	c, ok := cfg.Contexts[ctx]
	if !ok {
		return fmt.Errorf("no context named %s", ctx)
	}
	if c.Namespace == namespace {
		return nil
	}
	c.Namespace = namespace
	return clientcmd.ModifyConfig(rules, *cfg, true)
}

// ReadPrevious returns the previous namespace recorded for ctx, "" when none.
func ReadPrevious(dir, ctx string) string {
	data, err := os.ReadFile(filepath.Join(dir, ctx))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WritePrevious records the previous namespace of ctx in dir/ctx, the file
// kubens keeps too. The directory is created 0700 when missing.
func WritePrevious(dir, ctx, namespace string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return layout.WriteFileAtomic(filepath.Join(dir, ctx), []byte(namespace), 0o600)
}

// Client builds an HTTP client that authenticates as context ctx does, through
// client-go's transport (exec plugins, OIDC refresh and certificates included),
// and returns it with the API server's address.
func Client(files []string, ctx string, timeout time.Duration) (*http.Client, string, error) {
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: files}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: ctx}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restCfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", err
	}
	restCfg.Timeout = timeout
	client, err := rest.HTTPClientFor(restCfg)
	if err != nil {
		return nil, "", err
	}
	return client, strings.TrimRight(restCfg.Host, "/"), nil
}

// List returns the namespaces the cluster of context ctx has, sorted, through a
// GET that works through the read-only proxy too.
func List(ctx context.Context, files []string, name string, timeout time.Duration) ([]string, error) {
	client, host, err := Client(files, name, timeout)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/api/v1/namespaces", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot list namespaces (is the cluster reachable?): %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot list namespaces: the API server answered %s", resp.Status)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("cannot read the namespace list: %w", err)
	}
	out := make([]string, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, it.Metadata.Name)
	}
	sort.Strings(out)
	return out, nil
}

// Exists says whether namespace exists in the cluster of context name.
func Exists(ctx context.Context, files []string, name, namespace string, timeout time.Duration) (bool, error) {
	client, host, err := Client(files, name, timeout)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/api/v1/namespaces/"+namespace, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("cannot query namespace %s (is the cluster reachable?): %w", namespace, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("cannot query namespace %s: the API server answered %s", namespace, resp.Status)
}

// ProbeResult says what one authenticated request to the API server found.
type ProbeResult int

const (
	ProbeOK           ProbeResult = iota // the credentials work
	ProbeUnauthorized                    // 401: the login is gone
	ProbeCredentials                     // the credential layer failed before the request left (see credentialFailure)
	ProbeUnreachable                     // no answer, or an answer that says nothing about the login
)

// Probe makes one cheap authenticated request (GET /version) as context name.
// A 401 means the credentials no longer work, and so does a failure of the
// credential layer itself; a network failure or any other status says nothing
// about them.
func Probe(ctx context.Context, files []string, name string, timeout time.Duration) (ProbeResult, error) {
	client, host, err := Client(files, name, timeout)
	if err != nil {
		return ProbeUnreachable, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/version", nil)
	if err != nil {
		return ProbeUnreachable, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if credentialFailure(err) {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err // the reason, without the request it stopped
			}
			return ProbeCredentials, err
		}
		return ProbeUnreachable, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return ProbeUnauthorized, nil
	}
	return ProbeOK, nil
}

// credentialFailure recognises an error the credential layer raised before the
// request left the machine, so that a fresh fetch is the answer, as it is for a
// 401: client-go's OIDC provider refreshing a token the platform has rotated or
// revoked ("failed to refresh token: oauth2: ..."), or a credential plugin that
// failed ("getting credentials: exec: ..."). The texts are client-go's.
func credentialFailure(err error) bool {
	s := err.Error()
	for _, mark := range []string{"failed to refresh token", "oauth2:", "getting credentials", "exec plugin", "credential plugin"} {
		if strings.Contains(s, mark) {
			return true
		}
	}
	return false
}
