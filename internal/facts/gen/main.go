// Command gen writes the texts built from internal/facts/facts.yaml, and
// records a fact as reread against its code.
//
//	go run ./internal/facts/gen -write                # README.md and, where the website is, its facts.gen.json
//	go run ./internal/facts/gen -check                # fail when either is stale
//	go run ./internal/facts/gen -confirm "<id> <id>"  # confirmed.txt: these facts were reread
//
// Run it from the repository's top folder (the Makefile does).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/facts"
)

const (
	readmePath    = "README.md"
	confirmedPath = "internal/facts/confirmed.txt"
)

func main() {
	write := flag.Bool("write", false, "write README.md's sections and the website's facts.gen.json")
	check := flag.Bool("check", false, "fail when README.md's sections or the website's facts.gen.json are stale")
	confirm := flag.String("confirm", "", "space-separated fact ids that were reread against their code")
	flag.Parse()
	var err error
	switch {
	case *write:
		err = run(true)
	case *check:
		err = run(false)
	case *confirm != "":
		err = confirmFacts(strings.Fields(*confirm))
	default:
		err = errors.New("give -write, -check or -confirm")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "facts:", err)
		os.Exit(1)
	}
}

func run(write bool) error {
	f, err := facts.Load()
	if err != nil {
		return err
	}
	type out struct {
		path string
		want []byte
	}
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		return err
	}
	r, err := f.Readme(readme)
	if err != nil {
		return err
	}
	outs := []out{{readmePath, r}}
	if st, err := os.Stat(facts.WebsiteDir); err == nil && st.IsDir() {
		outs = append(outs, out{facts.SiteJSONPath, f.SiteJSON()})
	}
	var stale []string
	for _, o := range outs {
		got, _ := os.ReadFile(o.path)
		if bytes.Equal(got, o.want) {
			continue
		}
		if !write {
			stale = append(stale, o.path)
			continue
		}
		if err := os.WriteFile(o.path, o.want, 0o644); err != nil {
			return err
		}
		fmt.Println("facts: wrote", o.path)
	}
	if len(stale) > 0 {
		return fmt.Errorf("%s stale: edit internal/facts/facts.yaml, then run `make facts`", strings.Join(stale, " and "))
	}
	if !write {
		fmt.Println("facts: README.md and the site's facts are fresh")
	}
	return nil
}

// confirmFacts records the current hash of every code anchor of the given
// facts, and drops their old lines.
func confirmFacts(ids []string) error {
	f, err := facts.Load()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(confirmedPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	c, err := facts.ParseConfirmed(b)
	if err != nil {
		return err
	}
	root, _ := filepath.Abs(".")
	msgs, err := facts.ConfirmInto(c, f, facts.NewRepo(root), ids)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		fmt.Println("facts:", m)
	}
	return os.WriteFile(confirmedPath, c.Format(), 0o644)
}
