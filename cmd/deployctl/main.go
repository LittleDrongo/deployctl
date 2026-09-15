package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/LittleDrongo/deployctl/internal/cli"
)

// Set by the CLI release build, independently of application metadata.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdout, version); err != nil {
		fmt.Fprintln(os.Stderr, "deployctl:", err)
		os.Exit(1)
	}
}
