// Command gen writes or checks web/src/contract.gen.ts.
//
//	go run ./internal/contract/gen -out web/src/contract.gen.ts
//	go run ./internal/contract/gen -check web/src/contract.gen.ts
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/IZZY-Agency/sitboard/internal/contract"
)

func main() {
	out := flag.String("out", "", "write the TypeScript contract to this file")
	check := flag.String("check", "", "fail when this file differs from the generated contract")
	flag.Parse()

	ts := contract.TypeScript()
	switch {
	case *out != "":
		if err := os.WriteFile(*out, []byte(ts), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
	case *check != "":
		got, err := os.ReadFile(*check)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
		if string(got) != ts {
			fmt.Fprintf(os.Stderr, "gen: %s is stale, run `make contract`\n", *check)
			os.Exit(1)
		}
		fmt.Printf("gen: %s is fresh\n", *check)
	default:
		fmt.Print(ts)
	}
}
