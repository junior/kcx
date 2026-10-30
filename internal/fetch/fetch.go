// Package fetch runs a profile's provider command to obtain a cluster's
// kubeconfig, with the secret piped in and the lockout policy applied.
//
// The lockout policy is an invariant, not a feature: when the fetch command says
// the secret was refused, kcx tells the secret provider to forget it; if a human
// is there, kcx asks for a new one once and retries once; a second refusal, or no
// human, stops it. During a fetch of several clusters the first refusal aborts
// the rest. A failure that is not a refusal (the cluster unreachable, a timeout)
// keeps the cached secret and moves on. Two bad logins at most, ever, so an
// automated run can never lock an account.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/junior/kcx/v2/internal/profile"
	"github.com/junior/kcx/v2/internal/secret"
)

// DefaultTimeout bounds a provider command that sets none.
const DefaultTimeout = 120 * time.Second

// Outcome classifies one run of the provider command.
type Outcome int

const (
	OK       Outcome = iota // the kubeconfig was written
	Rejected                // the output matched the provider's rejected pattern: the secret was refused
	Failed                  // anything else: the cluster unreachable, a timeout, no file written
)

// Runner holds what a fetch needs from its surroundings.
type Runner struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Interactive says a human can answer a refresh prompt.
	Interactive bool
	// SecretCache false asks a caching provider to forget after every fetch.
	SecretCache bool
	Getenv      func(string) string
	LookPath    secret.LookPath
	// TTY, when set, is where the prompt provider reads (tests); /dev/tty otherwise.
	TTY io.Reader
	// Home is what ~ stands for in a script provider's path; PowerShell is the
	// powershell setting, the PowerShell that runs such a script.
	Home, PowerShell string
	// Environ is the environment the command runs with (os.Environ() when nil);
	// KUBECONFIG and KCX_OUT are always replaced.
	Environ []string

	// refreshFailed holds, by provider name, the refreshes that failed in this
	// run: a Runner is one run.
	refreshFailed map[string]bool
}

// Request is one cluster to fetch.
type Request struct {
	Set     *profile.Set
	Cluster *profile.Cluster
	User    string // -u: overrides the profile's user template
	Out     string // the file the command must write
}

// ErrRejected is returned when the secret was refused and the policy stopped.
var ErrRejected = errors.New("the secret was refused")

// Fetch obtains one kubeconfig into req.Out, applying the lockout policy.
func (r *Runner) Fetch(ctx context.Context, req Request) error {
	set, c := req.Set, req.Cluster
	p := set.ProviderOf(c)
	if p.Kind != "command" {
		return fmt.Errorf("cluster %s: provider %s is of kind %s, which this build cannot fetch yet", c.Name, p.Name, p.Kind)
	}
	host, user, err := set.Resolve(c, req.User, r.Getenv)
	if err != nil {
		return err
	}
	args, err := set.Command(c, host, user, req.Out, r.Getenv)
	if err != nil {
		return err
	}
	prov, err := secret.ResolveWith(p.Secret, secret.Options{
		LookPath: r.LookPath, TTY: r.TTY, Getenv: r.Getenv, Home: r.Home, PowerShell: r.PowerShell,
	})
	if err != nil {
		return err
	}
	if prov != nil {
		if e, ok := prov.(*secret.Exec); ok {
			e.Stderr = r.Stderr
		}
		if pr, ok := prov.(*secret.Prompt); ok {
			pr.Stderr = r.Stderr
		}
	}
	r.sayf("fetching %s (%s, user %s)", c.Name, host, user)
	retried := false
	for {
		outcome, asked, err := r.runOnce(ctx, p, args, prov, req.Out)
		switch outcome {
		case OK:
			if prov != nil && prov.Caches() && !r.SecretCache && !asked {
				_ = prov.Forget(ctx)
			}
			return nil
		case Failed:
			return err
		case Rejected:
			if prov == nil {
				return fmt.Errorf("cluster %s: %w: %v", c.Name, ErrRejected, err)
			}
			if asked {
				// The command asked the person itself: it may ask once more, and
				// never a third time.
				if retried {
					return fmt.Errorf("cluster %s: %w twice; no further try was made", c.Name, ErrRejected)
				}
				r.errf("the secret was refused; %s asks once more", filepath.Base(args[0]))
				retried = true
				continue
			}
			_ = prov.Forget(ctx)
			if retried || !r.Interactive || !prov.Caches() {
				return fmt.Errorf("cluster %s: %w; nothing was cached and no retry was made", c.Name, ErrRejected)
			}
			// Rotated or mistyped: ask once, right here, and retry once. Never a loop.
			r.errf("the secret was refused (rotated?); enter the new one:")
			if err := r.refresh(ctx, prov); err != nil {
				return fmt.Errorf("cluster %s: %w; %v", c.Name, ErrRejected, err)
			}
			retried = true
		}
	}
}

// runOnce runs the provider command once, with the secret on standard input
// when there is a provider, and classifies the result. asked says the provider
// could not run here and the command asked the person for the secret itself.
func (r *Runner) runOnce(ctx context.Context, p *profile.Provider, args []string, prov secret.Provider, out string) (outcome Outcome, asked bool, err error) {
	timeout := p.TimeoutDuration()
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	var stdin io.Reader = strings.NewReader("")
	cctx, cancel := context.WithCancel(ctx)
	if prov != nil {
		// The secret is produced before the command starts: a provider that has
		// nothing stops the fetch here, so a missing secret is never sent as a bad
		// login. The bytes wait in the pipe; a password is far smaller than a pipe.
		// Reading it is bounded by the timeout; asking for it is not, since a
		// person may take minutes to look a password up, and the command's own
		// timeout starts once the secret is there.
		secretReader, err := r.secretFor(ctx, prov, timeout)
		switch {
		case errors.Is(err, secret.ErrUnavailable):
			// The provider cannot run on this machine (a script that a locked-down
			// PowerShell refuses): the command asks the person itself, on the
			// terminal, and with no timeout, since a person is typing.
			if !r.Interactive {
				cancel()
				return Failed, false, fmt.Errorf("secret provider %s cannot run here, and there is no terminal for %s to ask on", prov.Name(), filepath.Base(args[0]))
			}
			r.sayf("secret provider %s cannot run here: %s asks for the secret itself", prov.Name(), filepath.Base(args[0]))
			stdin, asked = r.Stdin, true
		case err != nil:
			cancel()
			return Failed, false, err
		default:
			defer secretReader.Close()
			// Newline-terminated, as a program reading a line from a pipe expects.
			stdin = io.MultiReader(secretReader, strings.NewReader("\n"))
		}
	}
	if !asked {
		cancel()
		cctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	_ = os.Remove(out)
	cmd := exec.CommandContext(cctx, args[0], args[1:]...)
	// A process the command leaves behind holding its output (a helper it
	// started) would keep kcx waiting past the timeout, and after a command that
	// succeeded; WaitDelay closes the pipes and returns.
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = stdin
	var captured limitedBuffer
	cmd.Stdout = io.MultiWriter(r.Stdout, &captured)
	cmd.Stderr = io.MultiWriter(r.Stderr, &captured)
	cmd.Env = withEnv(r.environ(), map[string]string{"KUBECONFIG": out, "KCX_OUT": out})
	if err := cmd.Run(); err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return Failed, asked, fmt.Errorf("%s timed out after %s", args[0], timeout)
		}
		if re := p.RejectedRE(); re != nil && re.Match(captured.Bytes()) {
			return Rejected, asked, fmt.Errorf("%s: %s", args[0], firstLine(captured.Bytes()))
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return Failed, asked, fmt.Errorf("%s failed (exit %d); nothing was imported", args[0], ee.ExitCode())
		}
		return Failed, asked, fmt.Errorf("cannot run %s: %w", args[0], err)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		return Failed, asked, fmt.Errorf("%s wrote no kubeconfig: a tool that ignores KUBECONFIG needs {out} where its file option goes", args[0])
	}
	_ = os.Chmod(out, 0o600)
	return OK, asked, nil
}

// secretFor reads the provider's secret, asking for it once (refresh) when the
// provider has none and someone is there to answer. ErrUnavailable passes
// through: the provider cannot run on this machine at all.
func (r *Runner) secretFor(ctx context.Context, prov secret.Provider, timeout time.Duration) (io.ReadCloser, error) {
	rd, err := readSecret(ctx, prov, timeout)
	if err == nil || errors.Is(err, secret.ErrUnavailable) {
		return rd, err
	}
	if !r.Interactive || !prov.Caches() {
		return nil, fmt.Errorf("no secret for provider %s (%v); run kcx secret refresh %s from a terminal", prov.Name(), err, prov.Name())
	}
	if err := r.refresh(ctx, prov); err != nil {
		return nil, err
	}
	rd, err = readSecret(ctx, prov, timeout)
	if err != nil {
		r.refreshFailed[prov.Name()] = true
	}
	return rd, err
}

// refresh asks the person for a new secret, once per provider in a run: after
// a refresh that failed, the rest of the run (fetch --all) does not ask again
// for that provider, and its clusters fail at once. A provider that cannot run
// here is no failed refresh: its command asks instead.
func (r *Runner) refresh(ctx context.Context, prov secret.Provider) error {
	if r.refreshFailed == nil {
		r.refreshFailed = map[string]bool{}
	}
	if r.refreshFailed[prov.Name()] {
		return fmt.Errorf("no secret for provider %s, whose refresh failed earlier in this run; run kcx secret refresh %s, then fetch again", prov.Name(), prov.Name())
	}
	err := prov.Refresh(ctx, r.Stdin, r.Stderr, r.Stderr)
	if err != nil && !errors.Is(err, secret.ErrUnavailable) {
		r.refreshFailed[prov.Name()] = true
	}
	return err
}

// readSecret runs the provider's get within timeout and hands back the secret,
// waiting in its pipe.
func readSecret(ctx context.Context, prov secret.Provider, timeout time.Duration) (io.ReadCloser, error) {
	gctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rd, wait, err := prov.Get(gctx)
	if err != nil {
		return nil, err
	}
	if err := wait(); err != nil {
		_ = rd.Close()
		return nil, err
	}
	return rd, nil
}

func (r *Runner) environ() []string {
	if r.Environ != nil {
		return r.Environ
	}
	return os.Environ()
}

func (r *Runner) sayf(format string, a ...any) {
	if r.Stderr != nil {
		fmt.Fprintf(r.Stderr, "kcx: "+format+"\n", a...)
	}
}

func (r *Runner) errf(format string, a ...any) { r.sayf(format, a...) }

// withEnv returns env with the given variables replaced or added.
func withEnv(env []string, set map[string]string) []string {
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if _, replaced := set[k]; replaced {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// limitedBuffer keeps the first 64 KB of what a command printed, for the
// rejected pattern and the error line.
type limitedBuffer struct{ buf bytes.Buffer }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 64<<10 - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitedBuffer) Bytes() []byte { return l.buf.Bytes() }

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Many fetches several clusters in order. The first refusal aborts the rest; any
// other failure is reported and the next cluster is tried. It returns the
// clusters fetched, the ones that failed (with their errors), and whether it
// aborted.
func (r *Runner) Many(ctx context.Context, set *profile.Set, clusters []*profile.Cluster, user string, outFor func(*profile.Cluster) string, after func(*profile.Cluster) error) (done []string, failed map[string]error, aborted bool) {
	failed = map[string]error{}
	for i, c := range clusters {
		err := r.Fetch(ctx, Request{Set: set, Cluster: c, User: user, Out: outFor(c)})
		if err == nil && after != nil {
			err = after(c)
		}
		if err == nil {
			done = append(done, c.Name)
			continue
		}
		failed[c.Name] = err
		if errors.Is(err, ErrRejected) {
			rest := make([]string, 0, len(clusters)-i-1)
			for _, s := range clusters[i+1:] {
				rest = append(rest, s.Name)
			}
			if len(rest) > 0 {
				r.errf("aborting the remaining fetches (%s): the secret was refused, and retrying each cluster would risk a lockout", strings.Join(rest, " "))
			}
			return done, failed, true
		}
	}
	return done, failed, false
}
