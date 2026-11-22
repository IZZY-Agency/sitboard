// Package gitenv names the environment variables that send git to another
// repository, worktree, index, object store or config. The scan drops them
// before it runs git, so it reads the repository it is given with the user's
// own config.
package gitenv

import "strings"

// Names are the variables by name. GIT_CONFIG_KEY_<n> and GIT_CONFIG_VALUE_<n>
// count only with GIT_CONFIG_COUNT, which is among them.
var Names = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_SHALLOW_FILE", "GIT_GRAFT_FILE", "GIT_REPLACE_REF_BASE",
	"GIT_QUARANTINE_PATH", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
}

// Redirects reports whether the variable name is one of them, the numbered
// GIT_CONFIG_KEY_ and GIT_CONFIG_VALUE_ included.
func Redirects(name string) bool {
	for _, n := range Names {
		if n == name {
			return true
		}
	}
	return strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}
