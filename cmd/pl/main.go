package main

import (
	"os"

	"pellets/internal/cli"
)

var version = "dev"

func main() {
	app := cli.New(version)
	os.Exit(app.Run(os.Args[1:], os.Stdout, os.Stderr))
}
