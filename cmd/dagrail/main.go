package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/CongBao/dagrail/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	args := os.Args[1:]
	if err := cli.RunContext(ctx, args, os.Stdin, os.Stdout, os.Stderr); err != nil {
		report := cli.DescribeError(err)
		if cli.WantsJSONErrors(args) {
			if writeErr := cli.WriteErrorJSON(os.Stderr, err); writeErr != nil {
				fmt.Fprintln(os.Stderr, "dagrail:", report.Message)
			}
		} else {
			fmt.Fprintln(os.Stderr, "dagrail:", report.Message)
		}
		os.Exit(report.ExitCode)
	}
}
