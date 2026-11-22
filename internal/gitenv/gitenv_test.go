package gitenv

import "testing"

// The list is written out here, not taken from Names: dropping a variable
// from the production list fails this test (review 4, finding 5).
func TestRedirectsEveryVariable(t *testing.T) {
	for _, n := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_SHALLOW_FILE", "GIT_GRAFT_FILE", "GIT_REPLACE_REF_BASE",
		"GIT_QUARANTINE_PATH", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
		"GIT_CONFIG_KEY_3", "GIT_CONFIG_VALUE_3",
	} {
		if !Redirects(n) {
			t.Errorf("%s is not dropped", n)
		}
	}
	for _, n := range []string{"GIT_AUTHOR_NAME", "HOME", "PATH", "GIT_SSH_COMMAND"} {
		if Redirects(n) {
			t.Errorf("%s is dropped", n)
		}
	}
}
