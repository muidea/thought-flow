// designcheck verifies an engineering delivery using public bytes only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"thoughtflow/internal/pkg/documentsetstore"
)

func main() {
	expected := flag.String("expected-payload", "", "trusted public payload hash (optional)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: designcheck [-expected-payload sha256:...] delivery.zip")
		os.Exit(2)
	}
	raw, e := os.ReadFile(flag.Arg(0))
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	result, e := documentsetstore.VerifyPublicPackage(raw, *expected)
	if e != nil {
		fmt.Fprintln(os.Stderr, "public package verification failed:", e)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
