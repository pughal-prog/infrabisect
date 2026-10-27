// Command infrabisect finds the infrastructure change that broke production
// by binary-searching a merged timeline of git commits, Helm revisions and
// any other change source in a throwaway environment.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pughal-prog/infrabisect/internal/cli"
)

// Set by the release build via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "none"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], cli.IO{Stdout: os.Stdout, Stderr: os.Stderr}, cli.BuildInfo{Version: version, Commit: commit})
	stop()
	os.Exit(code)
}
