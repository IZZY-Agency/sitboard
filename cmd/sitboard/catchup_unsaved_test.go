package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
)

// With --no-state the catch-up list says its marker is not saved, so the page
// does not present a fresh list after a start as a first look; with a state
// file it does not.
func TestCatchupSaysWhenTheMarkerIsNotSaved(t *testing.T) {
	for _, noState := range []bool{true, false} {
		cfg := cfgWith(0, true)
		cfg.noState = noState
		r := startIn(t, cfg, t.TempDir())
		c := client(t, r)
		resp, err := c.Get("http://127.0.0.1:" + r.port + contract.PathCatchup)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		err = json.NewDecoder(resp.Body).Decode(&got)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("--no-state %v: %d, %v", noState, resp.StatusCode, err)
		}
		if u, _ := got["unsaved"].(bool); u != noState {
			t.Errorf("--no-state %v: unsaved is %v", noState, got["unsaved"])
		}
		r.stop(t)
	}
}
