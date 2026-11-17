// Package e2e runs the real binary.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
)

// startBinary builds bin/sitboard into a temp folder, runs it with an empty
// HOME, and returns the link it printed.
func startBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("windows is not targeted")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "sitboard")
	build := exec.Command("go", "build", "-o", bin, "./cmd/sitboard")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "--no-open", "--port", "0")
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_STATE_HOME="+filepath.Join(home, ".local", "state"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	link := make(chan string, 1)
	ended := make(chan string, 1)
	go func() {
		re := regexp.MustCompile(`http://127\.0\.0\.1:\d+/\S*`)
		sc := bufio.NewScanner(stdout)
		var seen strings.Builder
		for sc.Scan() {
			seen.WriteString(sc.Text() + "\n")
			if m := re.FindString(sc.Text()); m != "" {
				link <- m
				_, _ = io.Copy(io.Discard, stdout)
				return
			}
		}
		ended <- seen.String()
	}()
	select {
	case l := <-link:
		return l
	case out := <-ended:
		t.Fatalf("sitboard exited without printing a link:\n%s", out)
	case <-time.After(15 * time.Second):
		t.Fatal("no link printed within 15s")
	}
	return ""
}

// The client keeps cookies and opens the printed link first, the way a browser
// does, so this test keeps passing once the token layer is added.
func newClient(t *testing.T, link string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	resp, err := c.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	base := link[:strings.Index(link[len("http://"):], "/")+len("http://")]
	return c, base
}

func TestSkeletonServesEmptySnapshot(t *testing.T) {
	link := startBinary(t)
	c, base := newClient(t, link)

	resp, err := c.Get(base + contract.PathSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var snap contract.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if snap.Version != contract.Version {
		t.Fatalf("version %d", snap.Version)
	}
	if len(snap.Cards) != 0 {
		t.Fatalf("%d cards on an empty machine", len(snap.Cards))
	}
	if !strings.Contains(string(raw), `"cards":[]`) {
		t.Fatalf("cards must be an empty array, not null: %s", raw)
	}
	seen := map[contract.SourceKind]int{}
	for _, s := range snap.Sources {
		seen[s.Kind]++
	}
	for _, k := range contract.SourceKinds {
		if seen[k] != 1 {
			t.Errorf("source %q appears %d times, want 1", k, seen[k])
		}
	}
	if len(snap.Sources) != len(contract.SourceKinds) {
		t.Errorf("%d sources, want %d", len(snap.Sources), len(contract.SourceKinds))
	}
}

func TestSkeletonStreamsSnapshotOnConnect(t *testing.T) {
	link := startBinary(t)
	c, base := newClient(t, link)
	c.Timeout = 0

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+contract.PathEvents, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var event string
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			event = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			if event != contract.EventSnapshot {
				t.Fatalf("event %q, want %q", event, contract.EventSnapshot)
			}
			var snap contract.Snapshot
			if err := json.Unmarshal([]byte(v), &snap); err != nil {
				t.Fatal(err)
			}
			if snap.Version != contract.Version {
				t.Fatalf("version %d", snap.Version)
			}
			return
		}
	}
	t.Fatalf("stream ended without a snapshot: %v", sc.Err())
}

func TestSkeletonServesThePage(t *testing.T) {
	link := startBinary(t)
	c, base := newClient(t, link)
	resp, err := c.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("page content type %q", ct)
	}
}
