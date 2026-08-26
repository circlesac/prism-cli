package main

import (
	"context"
	"fmt"
	"os"

	"github.com/circlesac/prism-cli/internal/cli"
)

func main() {
	if err := cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, Version); err != nil {
		fmt.Fprintln(os.Stderr, "prism:", err)
		os.Exit(cli.ExitCode(err))
	}
}
