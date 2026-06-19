//go:build headless

package main

import (
	"fmt"
	"os"
)

// runFrontend (headless build) runs the API server only and blocks forever.
// Useful for development, CI, or environments without a display. Build with
// `-tags headless`.
func runFrontend(url string) {
	fmt.Println("NetStack Doctor (headless) serving at", url)
	fmt.Println("Press Ctrl+C to quit.")
	select {}
}

func requestQuit() { os.Exit(0) }
