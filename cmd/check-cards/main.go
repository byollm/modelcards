// SPDX-License-Identifier: Apache-2.0
// check-cards verifies card references and hash scopes after JSON Schema validation.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/byollm/modelcards/internal/cardcheck"
)

func main() {
	writeIndex := flag.Bool("write-index", false, "regenerate the display index after validating all cards")
	flag.Parse()
	paths := flag.Args()
	allCards := len(paths) == 0
	if *writeIndex && !allCards {
		fmt.Fprintln(os.Stderr, "-write-index requires the complete default card set")
		os.Exit(1)
	}
	if len(paths) == 0 {
		var err error
		paths, err = filepath.Glob("cards/*.json")
		if err != nil || len(paths) == 0 {
			fmt.Fprintln(os.Stderr, "no cards found")
			os.Exit(1)
		}
	}
	verifier := cardcheck.NewVerifier()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			err = verifier.Add(filepath.ToSlash(path), data)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("%s: references, serving gates, and speculative artifact hash scopes valid\n", path)
	}
	if *writeIndex {
		if err := cardcheck.WriteIndex("index.json", verifier.Entries(), time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if allCards {
		data, err := os.ReadFile("index.json")
		if err == nil {
			err = cardcheck.VerifyIndex(data, verifier.Entries())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("index.json: exact card hashes and display metadata valid")
	}
}
