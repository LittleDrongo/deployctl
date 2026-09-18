package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/LittleDrongo/deployctl/internal/cli"
)

// Set by the CLI release build, independently of application metadata.
var version = "dev"

func main() {
	if version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var args []string
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}
	if err := cli.Run(ctx, args, os.Stdout, version); err != nil {
		fmt.Fprintln(os.Stderr, "deployctl:", err)
		os.Exit(1)
	}
}
