// Command sentinel is the Sentinel controller and command-line tool.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/karimzakzouk/sentinel/internal/cli"
)

func main() {
	if err := cli.NewRoot().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
