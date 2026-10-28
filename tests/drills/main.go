// Command drills runs the week-8 operational drills against the real cexd
// binary and writes an evidence report: crash under load with retries,
// money checks, restore from a copied journal, and recovery time.
//
// This first version is only the entry point (a main package must define
// func main to link); the drills arrive with the pieces they need.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "usage: go run ./tests/drills -out <dir>")
	os.Exit(2)
}
