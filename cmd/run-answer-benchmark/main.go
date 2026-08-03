package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(context.Context, []string, io.Writer, io.Writer) int {
	fmt.Fprintln(os.Stderr, "run-answer-benchmark: not implemented")
	return 1
}
