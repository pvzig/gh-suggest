// Gh-suggest creates guarded pull-request reviews containing exact suggested changes.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pvzig/gh-suggest/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	return cli.New(cli.NewDefaultCreator(), cli.NewDefaultReconciler()).Run(
		ctx,
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
	)
}
