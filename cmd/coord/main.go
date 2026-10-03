package main

import (
	"fmt"
	"os"
)

func main() {
	root := newRoot()
	root.SetArgs(detachedArgs())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "coord:", err)
		os.Exit(1)
	}
}
