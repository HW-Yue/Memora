package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/HW-Yue/Memora/internal/acceptance"
)

func main() {
	var options acceptance.Options
	var timeout time.Duration
	flag.StringVar(&options.PublicationDir, "publication", "", "absolute publication directory")
	flag.StringVar(&options.OutputDir, "output", "", "absolute new acceptance output directory")
	flag.StringVar(&options.Version, "version", "", "stable semantic product version")
	flag.StringVar(&options.Arch, "arch", "", "native runner architecture")
	flag.DurationVar(&timeout, "timeout", 5*time.Minute, "journey timeout")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal("positional arguments are not supported")
	}
	options.PublicationDir = absolute(options.PublicationDir)
	options.OutputDir = absolute(options.OutputDir)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	report, err := acceptance.Run(ctx, options)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Printf(
		"Memora %s darwin/%s clean-machine acceptance %s (%d steps)\n",
		report.ProductVersion,
		report.Arch,
		report.Status,
		len(report.Steps),
	)
}

func absolute(value string) string {
	if value == "" {
		return ""
	}
	result, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	return filepath.Clean(result)
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "clean-machine acceptance:", message)
	os.Exit(1)
}
