package main

import (
	"fmt"
	"os"

	"github.com/dimmkirr/addiplay/cmd"
)

// Version lives in the cmd package — `cmd.Version` is what cobra reads
// for `--version`. The build-time ldflag is
// `-X github.com/dimmkirr/addiplay/cmd.Version=<value>`, which the
// linker writes directly into the cmd package's exported var BEFORE
// any init() runs. Don't add a `var Version` here — it's redundant and
// historically caused a subtle bug where the cmd-package version stayed
// "dev" because main()'s copy ran AFTER cmd's init() had already
// snapshotted the default value into rootCmd.Version.

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
