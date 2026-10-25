// kcx keeps one kubeconfig file per cluster under ~/.kube and gives the shell a
// context picker, namespaces, cluster profiles, a read-only shell for AI tools and
// temp kind clusters. The design: https://github.com/junior/kcx (README).
package main

import (
	"os"

	// Registers the auth-provider plugins (oidc) with client-go, as kubectl does;
	// without this import a kubeconfig that logs in with `auth-provider: oidc`
	// fails with "no Auth Provider found".
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/junior/kcx/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
