package main

import (
	"os"

	"github.com/Citation-Media/wp-ssh-bridge/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
