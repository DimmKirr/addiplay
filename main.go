package main

import (
	"fmt"
	"os"

	"github.com/dimmkirr/addiplay/cmd"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	cmd.Version = Version
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
