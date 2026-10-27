// Command loadgen offers a reproducible, open-model trading workload to a
// running cexd and reports what the client saw AND what the server measured
// about itself, stage by stage (week 7).
//
// This first version is only the entry point; the subcommands arrive with
// the pieces they need (workload, client, scheduler, scraper, report).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "usage: loadgen tokens <workload.json> | loadgen run [flags]")
	os.Exit(2)
}
