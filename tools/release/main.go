package main

import (
	"flag"
	"fmt"
	"os"

	"incidentctl/internal/release"
)

func main() {
	var options release.Options
	flag.StringVar(&options.Root, "root", ".", "TUI source directory")
	flag.StringVar(&options.Version, "version", "", "release tag, e.g. v0.1.0")
	flag.StringVar(&options.Targets, "targets", release.DefaultTargets, "comma-separated OS/architecture targets")
	flag.StringVar(&options.Output, "output", "", "new output directory; default dist/<version>")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected release arguments")
		os.Exit(2)
	}
	output, err := release.Build(options)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
	fmt.Println("Distributions:", output)
}
