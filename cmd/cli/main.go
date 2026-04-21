package main

import (
	"fmt"
	"os"

	"github.com/EduardMaghakyan/gatr-cli/cmd/cli/internal/cli"
)

// Version is overridden at build time via -ldflags "-X main.Version=...".
var Version = "0.0.0-dev"

func main() {
	if err := cli.NewRoot(Version).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
