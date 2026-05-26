package main

import (
	"github.com/theomorin/dbpilot/internal/cli"
)

var version = "dev"

func main() {
	cli.SetVersion(version)
	cli.Execute()
}
