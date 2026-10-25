#!/usr/bin/env bash
# demo-setup.sh - the environment the README demo (demo.tape) records in: a home of
# its own under /tmp/demo, kcx built from this checkout, the example profile's acme
# platform, a stand-in platform command and secret provider, and a kind cluster on
# the local Docker engine that stands in for every cluster of the profile. Nothing
# touches the real home. Then, from the repo root: vhs .github/demo.tape
# To clean up: kind delete cluster --name kcx-demo && rm -rf /tmp/demo
#
# On a Mac where Google Chrome quits half a minute into a headless session (seen
# with Chrome 157 and its updater), hide it from VHS's launcher so VHS fetches a
# plain Chromium of its own, and turn the browser's own sandbox off inside the
# profile, in one line:
#   VHS_NO_SANDBOX=true sandbox-exec -p '(version 1) (allow default) (deny file-read* (subpath "/Applications/Google Chrome.app") (subpath "/Applications/Google Chrome Canary.app"))' vhs .github/demo.tape
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
home=/tmp/demo
version=$(git -C "$here" describe --tags --always 2>/dev/null | sed 's/^v//')

mkdir -p "$home/bin" "$home/.kube/configs" "$home/.config/kcx/profiles"
chmod 700 "$home/.kube" "$home/.kube/configs"
go build -C "$here" -trimpath -ldflags "-s -w -X github.com/junior/kcx/internal/cli.version=$version" -o "$home/bin/kcx" ./cmd/kcx

# The example's acme platform (examples/profiles/acme.toml without the cloud CLIs).
cat > "$home/.config/kcx/profiles/acme.toml" <<'EOF'
[provider.acme]
command  = ["acme", "get-kubeconfig", "{host}", "-u", "{user}", "-c", "{out}"]
secret   = "acme-secret"
host     = "{name}.k8s.acme.example"
user     = ["{env.ACME_USER}", "{env.USER}"]
rejected = "401|access_denied|invalid username or password"

[group.production]
label    = "Production"
provider = "acme"
color    = "red"
readonly = true

[group.lab]
label    = "Lab"
provider = "acme"
color    = "green"

[cluster.prod1]
group   = "production"
site    = "austin"
aliases = ["main"]

[cluster.prod2]
group = "production"
site  = "atlanta"

[cluster.office2-lab]
group = "lab"
site  = "bastrop"

[cluster.office7-lab]
group = "lab"
site  = "bastrop"
EOF
chmod 600 "$home/.config/kcx/profiles/acme.toml"

# A stand-in for a platform's kubeconfig command: the password arrives on standard
# input, the kubeconfig goes to the file after -c, and every cluster is the kind one.
cat > "$home/bin/acme" <<'EOF'
#!/bin/sh
out=
while [ $# -gt 0 ]; do [ "$1" = "-c" ] && out=$2; shift; done
IFS= read -r password
[ "$password" = "hunter2" ] || { echo "401 Unauthorized" >&2; exit 1; }
sleep 1
cp /tmp/demo/kind.yaml "$out"
EOF
# A stand-in secret provider, with kcx's four verbs.
cat > "$home/bin/acme-secret" <<'EOF'
#!/bin/sh
case "${1-}" in
  get) printf 'hunter2\n' ;;
  status) echo "acme-secret: a password is cached" ;;
  refresh|forget) ;;
  *) exit 2 ;;
esac
EOF
chmod 755 "$home/bin/acme" "$home/bin/acme-secret"

# The shell the recording types into.
cat > "$home/.zshrc" <<'EOF'
setopt interactivecomments
PROMPT='%F{cyan}$%f '
export PATH=/tmp/demo/bin:$PATH
export ACME_USER=jane
unset KUBECONFIG KCX_AUTO
eval "$(kcx init zsh)"
EOF

# The cluster behind every name in the profile.
if ! kind get clusters 2>/dev/null | grep -qx kcx-demo; then
  kind create cluster --name kcx-demo --kubeconfig "$home/kind.yaml" --wait 120s
fi
[ -f "$home/kind.yaml" ] || kind export kubeconfig --name kcx-demo --kubeconfig "$home/kind.yaml"
for ns in payments search; do
  kubectl --kubeconfig "$home/kind.yaml" get namespace "$ns" >/dev/null 2>&1 || kubectl --kubeconfig "$home/kind.yaml" create namespace "$ns"
done
echo "demo home ready at $home (kcx $version); now: vhs .github/demo.tape"
