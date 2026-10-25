package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/layout"
	"github.com/junior/kcx/internal/temp"
)

// newEngine makes the Docker engine; tests replace it with a fake.
var newEngine = func() temp.Engine { return temp.Docker{} }

// lookPath finds programs; tests replace it.
var lookPath = exec.LookPath

var engineName = regexp.MustCompile(`^[A-Za-z0-9_.+-]+$`)

// cmdTemp is `kcx temp create|ls|rm|prune|add`.
func (a *App) cmdTemp(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "create":
		return a.tempCreate(args)
	case "ls", "list":
		if len(args) != 0 {
			return usagef("kcx temp ls takes no arguments")
		}
		return a.tempLs()
	case "rm", "remove", "delete":
		if len(args) != 1 {
			return usagef("kcx temp rm NAME")
		}
		return a.tempRm(args[0])
	case "prune":
		if len(args) != 0 {
			return usagef("kcx temp prune takes no arguments")
		}
		return a.tempPrune()
	case "add":
		return a.cmdAdd("temp", args)
	case "":
		return usagef("kcx temp create|ls|rm|prune|add")
	}
	return usagef("kcx temp: unknown command %s", sub)
}

// tempCreate is `kcx temp create NAME [--engine CTX] [--config PRESET|FILE] [--use] [-- KIND_ARGS...]`.
func (a *App) tempCreate(args []string) error {
	name, engine, config, use := "", "", "", false
	var extra []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--engine":
			if i+1 >= len(args) {
				return usagef("--engine needs a docker context")
			}
			i++
			engine = args[i]
		case strings.HasPrefix(arg, "--engine="):
			engine = strings.TrimPrefix(arg, "--engine=")
		case arg == "--config":
			if i+1 >= len(args) {
				return usagef("--config needs a preset or a file")
			}
			i++
			config = args[i]
		case strings.HasPrefix(arg, "--config="):
			config = strings.TrimPrefix(arg, "--config=")
		case arg == "--use":
			use = true
		case arg == "--":
			extra = append([]string{}, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(arg, "-"):
			return usagef("unknown option %s (kind's own options go after --)", arg)
		default:
			if name != "" {
				return usagef("kcx temp create NAME [options]")
			}
			name = arg
		}
	}
	if name == "" {
		return usagef("kcx temp create needs a NAME")
	}
	if !layout.ValidKindName(name) {
		return fmt.Errorf("%s: kind takes lowercase letters, digits, . and -, up to 40", name)
	}
	for _, prog := range []string{"kind", "docker"} {
		if _, err := lookPath(prog); err != nil {
			return fmt.Errorf("%s is not installed", prog)
		}
	}
	e := newEngine()
	if f, err := temp.FindFile(a.lay.Temp, name); err != nil {
		return err
	} else if f != "" {
		return fmt.Errorf("a temp cluster named %s exists already (kcx temp ls)", name)
	}
	if engine == "" {
		engine = a.getenv("DOCKER_CONTEXT")
	}
	if engine == "" {
		ctxs, err := e.Contexts()
		if err != nil {
			return err
		}
		for _, c := range ctxs {
			if c.Current {
				engine = c.Name
			}
		}
	}
	if engine == "" {
		return fmt.Errorf("no docker context to run on; name one with --engine")
	}
	if !engineName.MatchString(engine) {
		return fmt.Errorf("%s is not a docker context name", engine)
	}
	preset := ""
	if config != "" {
		switch {
		case fileExists(config):
			preset = config
		case fileExists(filepath.Join(a.lay.Presets, config+".yaml")):
			preset = filepath.Join(a.lay.Presets, config+".yaml")
		default:
			return fmt.Errorf("no kind preset or file %s; presets: %s", config, strings.Join(temp.Presets(a.lay.Presets), " "))
		}
	}
	// kind writes a cluster, a user and a context all named kind-NAME: they must
	// not exist in any file yet.
	kindName := "kind-" + name
	if err := entriesFree(a.lay, []kubeconfig.Entry{{Kind: "cluster", Name: kindName}, {Kind: "user", Name: kindName}, {Kind: "context", Name: kindName}}); err != nil {
		return err
	}
	if running, err := e.Clusters(engine); err == nil {
		for _, r := range running {
			if r == name {
				return fmt.Errorf("a kind cluster named %s already runs on %s (made from another machine?)", name, engine)
			}
		}
	}
	addr, err := temp.Address(e, engine)
	if err != nil {
		return err
	}
	if err := a.lay.Ensure(); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "kcx.")
	if err != nil {
		return fmt.Errorf("mktemp failed: %w", err)
	}
	defer os.RemoveAll(work)
	file := a.lay.TempFile(name, engine)
	cfgPath := preset
	tries := 1
	fixedPort := 0
	if addr != "" {
		// A remote engine: kind would bind the API server to 127.0.0.1 on THAT
		// machine, which nothing here can reach, so it listens on the engine's
		// address instead (kind adds that address to the API server's certificate).
		// kind finds a free port by binding the address locally, which fails for a
		// remote one, so kcx chooses a port the engine does not publish, and tries
		// again on a collision.
		cfgPath = filepath.Join(work, "kind.yaml")
		if preset != "" {
			pa, pp, err := temp.PresetAddressAndPort(preset)
			if err != nil {
				return err
			}
			if temp.IsLoopback(pa) {
				a.warn("the preset binds the API server to %s; on %s that is %s instead", pa, engine, addr)
			} else if pa != "" {
				addr = pa
			}
			fixedPort = pp
		}
		if fixedPort == 0 {
			tries = 3
		}
	}
	for {
		port := fixedPort
		if addr != "" {
			if port == 0 {
				if port, err = temp.FreePort(e, engine); err != nil {
					return err
				}
			}
			data, err := temp.AimConfig(preset, addr, port)
			if err != nil {
				return err
			}
			if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
				return err
			}
		}
		kargs := []string{"--name", name, "--kubeconfig", file}
		if cfgPath != "" {
			kargs = append(kargs, "--config", cfgPath)
		}
		kargs = append(kargs, extra...)
		out, err := e.Create(engine, kargs, a.stderr)
		if err == nil {
			break
		}
		_ = os.Remove(file)
		tries--
		if tries > 0 && temp.PortCollision(out) {
			a.warn("port %d is taken on %s; trying another", port, engine)
			continue
		}
		return fmt.Errorf("kind could not create %s on %s", name, engine)
	}
	// Managed files carry no current-context, and are private.
	cfg, err := kubeconfig.LoadFile(file)
	if err != nil {
		return err
	}
	cfg.CurrentContext = ""
	if err := kubeconfig.WriteFile(file, cfg); err != nil {
		return err
	}
	ctx := "kind-" + name
	a.say("temp cluster %s is up on %s: context %s, server %s", name, engine, ctx, kubeconfig.ServerOf(cfg, ctx))
	if use {
		if err := a.runGuard(); err != nil {
			return err
		}
		if err := a.use(ctx); err != nil {
			return err
		}
		a.say("switched to %s", ctx)
	}
	a.hintNew(ctx, file)
	a.say("kcx temp rm %s deletes it", name)
	return nil
}

// entriesFree refuses when any of the entries exists in a file of the layout.
func entriesFree(lay layout.Layout, entries []kubeconfig.Entry) error {
	for _, f := range lay.Files() {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		cfg, err := kubeconfig.LoadFile(f)
		if err != nil {
			return err
		}
		for _, have := range kubeconfig.Entries(cfg) {
			for _, want := range entries {
				if have == want {
					return fmt.Errorf("a %s named %s already exists in %s; choose another name", have.Kind, have.Name, f)
				}
			}
		}
	}
	return nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func (a *App) tempLs() error {
	states := temp.States(newEngine(), a.lay.Temp)
	if len(states) == 0 {
		a.warn("no temp clusters (kcx temp create NAME)")
		return nil
	}
	rows := [][]string{{"NAME", "ENGINE", "STATE", "CONTEXT", "SERVER"}}
	for _, s := range states {
		ctx, server := "-", "-"
		if cfg, err := kubeconfig.LoadFile(s.File); err == nil {
			ctxs, _ := kubeconfig.ContextsIn(s.File)
			if len(ctxs) > 0 {
				ctx = ctxs[0]
				server = kubeconfig.ServerOf(cfg, ctx)
			}
		}
		rows = append(rows, []string{s.Name, s.Engine, s.State, ctx, server})
	}
	writeColumns(a.stdout, rows)
	return nil
}

// forgetTemp clears the current context when it is one of the file's, then
// deletes the file (a temp cluster's credentials die with the cluster).
func (a *App) forgetTemp(file string, ctxs []string) error {
	cur, err := a.store().Current()
	if err == nil && cur != "" && containsString(ctxs, cur) {
		if err := kubeconfig.ClearCurrent(a.lay.State); err != nil {
			return err
		}
		a.say("%s was the current context; there is none now", cur)
	}
	return os.Remove(file)
}

func (a *App) tempRm(name string) error {
	file, err := temp.FindFile(a.lay.Temp, name)
	if err != nil {
		return err
	}
	if file == "" {
		return fmt.Errorf("no temp cluster named %s (kcx temp ls)", name)
	}
	ctxs, _ := kubeconfig.ContextsIn(file) // before kind delete empties the file
	_, engine := layout.SplitTemp(file)
	e := newEngine()
	if engine != "" {
		if _, err := e.Clusters(engine); err != nil {
			return fmt.Errorf("%s does not answer; the kubeconfig stays (%s). kcx temp prune clears it once the cluster is gone", engine, file)
		}
		if err := e.Delete(engine, name, file, a.stderr); err != nil {
			return fmt.Errorf("kind could not delete %s on %s; the kubeconfig stays (%s)", name, engine, file)
		}
	} else {
		a.warn("%s was made by another tool: delete the cluster with that tool; this removes only its kubeconfig", name)
	}
	if err := a.forgetTemp(file, ctxs); err != nil {
		return err
	}
	a.say("removed temp cluster %s", name)
	return nil
}

func (a *App) tempPrune() error {
	n := 0
	for _, s := range temp.States(newEngine(), a.lay.Temp) {
		switch s.State {
		case "gone":
			ctxs, _ := kubeconfig.ContextsIn(s.File)
			if err := a.forgetTemp(s.File, ctxs); err != nil {
				return err
			}
			a.say("removed the kubeconfig of %s, gone from %s", s.Name, s.Engine)
			n++
		case "unknown":
			a.warn("%s does not answer; kept %s", s.Engine, s.File)
		}
	}
	if n == 0 {
		a.say("nothing to prune")
	}
	return nil
}

const pickTemp = "+ new temp cluster (kind)"

// tempWizard is the picker's "+ new temp cluster": the engine, then the config,
// then a name. ESC goes back a step, and at the first step cancels.
func (a *App) tempWizard() error {
	if _, err := lookPath("kind"); err != nil {
		return fmt.Errorf("kind is not installed")
	}
	p := a.picker()
	e := newEngine()
	ctxs, err := e.Contexts()
	if err != nil {
		return err
	}
	step := "engine"
	engine, cfg := "", ""
	for {
		switch step {
		case "engine":
			var lines []string
			for _, c := range ctxs {
				desc := c.Endpoint
				if c.Current {
					desc += "  (current)"
				}
				lines = append(lines, c.Name+"\t"+desc)
			}
			engine, err = p.Pick(lines, "Engine> ", "which Docker engine runs the cluster"+sep+"ESC cancels")
			if err != nil || engine == "" {
				return err
			}
			step = "config"
		case "config":
			lines := []string{"-\tkind's own: one node"}
			for _, pr := range temp.Presets(a.lay.Presets) {
				lines = append(lines, pr+"\t"+filepath.Join(a.lay.Presets, pr+".yaml"))
			}
			cfg, err = p.Pick(lines, "Config> ", "on "+engine+sep+"ESC back")
			if err != nil {
				return err
			}
			if cfg == "" {
				step = "engine"
				continue
			}
			step = "name"
		case "name":
			tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			if err != nil {
				return fmt.Errorf("no terminal to ask the name on")
			}
			fmt.Fprint(tty, "Name for the temp cluster (lowercase, digits, - and .; empty goes back): ")
			name, _ := bufio.NewReader(tty).ReadString('\n')
			_ = tty.Close()
			name = strings.TrimSpace(name)
			if name == "" {
				step = "config"
				continue
			}
			args := []string{name, "--engine", engine}
			if cfg != "-" {
				args = append(args, "--config", cfg)
			}
			return a.tempCreate(args)
		}
	}
}
