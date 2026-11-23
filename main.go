package main

import (
	"fmt"
	"os"
)

func main() {
	a, err := newApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(a.run(os.Args[1:]))
}
