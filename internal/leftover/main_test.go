package leftover

import (
	"os"
	"testing"
)

// TestMain gives the package's git a home of its own: sitboard's git drops
// GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM (gitenv), so without it the
// scan in these tests would read the developer's own ~/.gitconfig.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "leftover-home")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}
	if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
