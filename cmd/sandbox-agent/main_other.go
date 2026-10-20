//go:build !linux

// Command sandbox-agent only runs inside Linux guests.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "sandbox-agent runs only inside Linux microVMs; build with GOOS=linux")
	os.Exit(1)
}
