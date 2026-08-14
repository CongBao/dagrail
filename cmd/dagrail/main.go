package main

import (
	"fmt"
	"os"

	"github.com/CongBao/dagrail/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "dagrail:", err)
		os.Exit(1)
	}
}
