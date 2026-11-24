package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("golden", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// decodeStrict fails on keys the Go type does not know, so a golden file
// cannot drift ahead of the types.
func decodeStrict(t *testing.T, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestGoldenRoundTrips(t *testing.T) {
	cases := []struct {
		file string
		v    any
	}{
		// The file names are stable names, not the contract version (see Version).
		{"snapshot.v1.json", &Snapshot{}},
		{"catchup.v1.json", &Catchup{}},
		{"leftovers.v1.json", &Leftovers{}},
		{"issues.v1.json", &Issues{}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			data := readGolden(t, c.file)
			decodeStrict(t, data, c.v)
			out, err := json.MarshalIndent(c.v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, '\n')
			if !bytes.Equal(out, data) {
				t.Fatalf("round trip differs from golden/%s:\n%s", c.file, out)
			}
		})
	}
}

func TestGoldenHasEveryColumnAndAttention(t *testing.T) {
	var s Snapshot
	decodeStrict(t, readGolden(t, "snapshot.v1.json"), &s)

	if s.Version != Version {
		t.Fatalf("golden version %d, want %d", s.Version, Version)
	}
	cols := map[Column]bool{}
	att := map[Attention]bool{}
	unknownStatus := false
	for _, c := range s.Cards {
		cols[c.Column] = true
		for _, a := range c.Attention {
			att[a] = true
		}
		if c.Agent.Status == AgentStatusUnknown {
			unknownStatus = true
		}
	}
	for _, c := range Columns {
		if !cols[c] {
			t.Errorf("golden has no card in column %q", c)
		}
	}
	for _, a := range Attentions {
		if !att[a] {
			t.Errorf("golden has no card with attention %q", a)
		}
	}
	if !unknownStatus {
		t.Error("golden has no agent with status unknown")
	}
	ready := map[bool]bool{}
	for _, src := range s.Sources {
		ready[src.Ready] = true
	}
	if !ready[true] || !ready[false] {
		t.Error("golden needs one source that is ready and one that is still on its first pass")
	}
	if len(s.Sources) != len(SourceKinds) {
		t.Errorf("golden has %d sources, want one per kind (%d)", len(s.Sources), len(SourceKinds))
	}
}

// The golden files are what the UI's dev harness renders, so they hold only
// the placeholder values listed in testdata/fixtures/README.md.
func TestGoldenUsesOnlyPlaceholders(t *testing.T) {
	allowedNames := regexp.MustCompile(`^(coordinator-a|worker-[a-f]|solo-a|)$`)
	for _, f := range []string{"snapshot.v1.json", "catchup.v1.json", "leftovers.v1.json"} {
		data := string(readGolden(t, f))
		for _, m := range regexp.MustCompile(`"(?:name|agentName)": "([^"]*)"`).FindAllStringSubmatch(data, -1) {
			if !allowedNames.MatchString(m[1]) && !strings.HasPrefix(m[1], "alpha") && m[1] != "beta" && m[1] != "gamma" {
				t.Errorf("%s: name %q is not a placeholder", f, m[1])
			}
		}
		for _, m := range regexp.MustCompile(`"repo": "([^"]*)"`).FindAllStringSubmatch(data, -1) {
			if m[1] != "" && m[1] != "example-org/example-repo" && m[1] != "example-group/example-project" {
				t.Errorf("%s: repo %q is not a placeholder", f, m[1])
			}
		}
		for _, m := range regexp.MustCompile(`"(?:host|repoHost)": "([^"]*)"|"gitlabHosts": \[\s*"([^"]*)"`).FindAllStringSubmatch(data, -1) {
			if h := m[1] + m[2]; h != "" && h != "github.com" && h != "gitlab.example.com" {
				t.Errorf("%s: host %q is not a placeholder", f, h)
			}
		}
		for _, m := range regexp.MustCompile(`"url": "https://([^/"]*)/`).FindAllStringSubmatch(data, -1) {
			if m[1] != "github.com" && m[1] != "gitlab.example.com" && m[1] != "claude.ai" {
				t.Errorf("%s: link host %q is not a placeholder", f, m[1])
			}
		}
		for _, m := range regexp.MustCompile(`"(?:path|folder)": "([^"]*)"`).FindAllStringSubmatch(data, -1) {
			if m[1] != "" && !strings.HasPrefix(m[1], "~/projects/") {
				t.Errorf("%s: path %q is not a placeholder", f, m[1])
			}
		}
	}
}

func TestCardIDFormat(t *testing.T) {
	got, err := CardID("claude", "1234")
	if err != nil || got != "claude:1234" {
		t.Fatalf("CardID = %q, %v", got, err)
	}
	uuid := "11111111-1111-4111-8111-111111111111"
	if got, err := CardID("codex", uuid); err != nil || got != "codex:"+uuid {
		t.Fatalf("CardID(uuid) = %q, %v", got, err)
	}
	if _, err := CardID("claude", strings.Repeat("a", 128)); err != nil {
		t.Fatalf("128 characters refused: %v", err)
	}
}

// A session id comes from records on disk and is untrusted. Ids reach the page
// and the state file, so anything outside the safe alphabet is refused.
func TestCardIDRefusesUnsafeSessionIDs(t *testing.T) {
	for _, id := range []string{
		"",
		strings.Repeat("a", 129),
		"../../etc",
		"a/b",
		"a:b",
		"a b",
		"<script>",
		"a\nb",
		"é",
	} {
		if got, err := CardID("claude", id); err == nil {
			t.Errorf("CardID(%q) = %q, want an error", id, got)
		} else if !errors.Is(err, ErrBadSessionID) {
			t.Errorf("CardID(%q) error %v, want ErrBadSessionID", id, err)
		}
	}
}

// Card ids come back from the page and are untrusted: ParseCardID accepts
// only what CardID produces.
func TestParseCardID(t *testing.T) {
	uuid := "11111111-1111-4111-8111-111111111111"
	for _, tool := range Tools {
		id, err := CardID(string(tool), uuid)
		if err != nil {
			t.Fatal(err)
		}
		gotTool, gotSession, ok := ParseCardID(id)
		if !ok || gotTool != tool || gotSession != uuid {
			t.Fatalf("ParseCardID(%q) = %q, %q, %v", id, gotTool, gotSession, ok)
		}
	}
	for _, id := range []string{
		"",
		"claude",
		"claude:",
		":1234",
		"other:1234",
		"Claude:1234",
		"claude:12:34",
		"claude:../x",
		"claude:a b",
		"claude:" + strings.Repeat("a", 129),
		" claude:1234",
	} {
		if tool, session, ok := ParseCardID(id); ok {
			t.Errorf("ParseCardID(%q) = %q, %q, true; want refused", id, tool, session)
		}
	}
}

// Every typed string constant in contract.go must be listed in its exported
// value slice, so the generated TypeScript cannot silently miss one.
func TestEveryEnumConstantIsInItsSlice(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "contract.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]map[string]bool{}
	for _, e := range enumList() {
		listed[e.name] = map[string]bool{}
		for _, v := range e.values {
			listed[e.name][v] = true
		}
	}
	stringTypes := 0
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			switch sp := spec.(type) {
			case *ast.TypeSpec:
				if id, ok := sp.Type.(*ast.Ident); ok && id.Name == "string" {
					stringTypes++
					if _, ok := listed[sp.Name.Name]; !ok {
						t.Errorf("string type %s has no entry in enumList", sp.Name.Name)
					}
				}
			case *ast.ValueSpec:
				typ, ok := sp.Type.(*ast.Ident)
				if gd.Tok != token.CONST || !ok {
					continue
				}
				for i, name := range sp.Names {
					lit, ok := sp.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !listed[typ.Name][v] {
						t.Errorf("constant %s (%s %q) is missing from its value slice", name.Name, typ.Name, v)
					}
				}
			}
		}
	}
	if stringTypes != len(enumList()) {
		t.Errorf("contract.go has %d string types, enumList has %d", stringTypes, len(enumList()))
	}
}

func TestProjectIDIsStableAndHidesThePath(t *testing.T) {
	root := "/home/dev/projects/alpha"
	a, b := ProjectID(root), ProjectID(root)
	if a != b {
		t.Fatalf("not stable: %q vs %q", a, b)
	}
	if ProjectID(root+"/") != a {
		t.Fatal("a trailing slash changes the id")
	}
	if ProjectID("/home/dev/projects/beta") == a {
		t.Fatal("two roots share an id")
	}
	if !regexp.MustCompile(`^p_[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("id %q has the wrong shape", a)
	}
	for _, part := range strings.Split(strings.Trim(root, "/"), "/") {
		if strings.Contains(a[2:], part) {
			t.Fatalf("id %q contains path part %q", a, part)
		}
	}
}

func TestGeneratedTypesAreFresh(t *testing.T) {
	want := TypeScript()
	got, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "contract.gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatal("web/src/contract.gen.ts is stale: run `make contract`")
	}
}

func TestEveryEnumHasValues(t *testing.T) {
	for _, e := range enumList() {
		if len(e.values) == 0 {
			t.Errorf("enum %s has no values", e.name)
		}
	}
}

// The page checks artifact links against the contract's pattern, the adapters
// against the model's; they must be one rule.
func TestArtifactURLPatternMatchesTheModel(t *testing.T) {
	if ArtifactURLPattern != model.ArtifactURLPattern {
		t.Fatalf("contract.ArtifactURLPattern %q != model.ArtifactURLPattern %q", ArtifactURLPattern, model.ArtifactURLPattern)
	}
	if !strings.Contains(TypeScript(), "export const ARTIFACT_URL_PATTERN = new RegExp(") {
		t.Fatal("the generated types do not carry ARTIFACT_URL_PATTERN")
	}
}

func TestValidProjectID(t *testing.T) {
	for _, root := range []string{"/", "/home/dev/work", "relative", ""} {
		if id := ProjectID(root); !ValidProjectID(id) {
			t.Errorf("ProjectID(%q) = %q is not valid", root, id)
		}
	}
	for _, id := range []string{"", "p_", "p_0123456789a", "p_0123456789abc", "p_0123456789AB", "q_0123456789ab", " p_0123456789ab", "p_0123456789ab\n", "claude:1234"} {
		if ValidProjectID(id) {
			t.Errorf("%q is valid", id)
		}
	}
}

func TestPullRequestAndIssueURLPatternsMatchTheModel(t *testing.T) {
	if PullRequestURLPattern != model.PullRequestURLPattern {
		t.Fatalf("contract.PullRequestURLPattern %q != model.PullRequestURLPattern %q", PullRequestURLPattern, model.PullRequestURLPattern)
	}
	if IssueURLPattern != model.IssueURLPattern {
		t.Fatalf("contract.IssueURLPattern %q != model.IssueURLPattern %q", IssueURLPattern, model.IssueURLPattern)
	}
	if MergeRequestURLPattern != model.MergeRequestURLPattern {
		t.Fatalf("contract.MergeRequestURLPattern %q != model.MergeRequestURLPattern %q", MergeRequestURLPattern, model.MergeRequestURLPattern)
	}
	if GitLabIssueURLPattern != model.GitLabIssueURLPattern {
		t.Fatalf("contract.GitLabIssueURLPattern %q != model.GitLabIssueURLPattern %q", GitLabIssueURLPattern, model.GitLabIssueURLPattern)
	}
	if string(ForgeGitHub) != string(model.ForgeGitHub) || string(ForgeGitLab) != string(model.ForgeGitLab) {
		t.Fatal("contract forges differ from the model's")
	}
}

// The golden snapshot carries a GitLab merge request so the page's dev
// harness and tests meet the shape; every link in it is the one the model
// builds from forge, host, repo and number, and every GitLab host is listed.
func TestGoldenLinksAreTheBuiltOnes(t *testing.T) {
	var s Snapshot
	decodeStrict(t, readGolden(t, "snapshot.v1.json"), &s)
	repoOf := func(f Forge, host, path string) model.ForgeRepo {
		return model.ForgeRepo{Forge: model.Forge(f), Host: host, Path: path}
	}
	forges := map[Forge]bool{}
	for _, c := range s.Cards {
		for _, pr := range c.PullRequests {
			forges[pr.Forge] = true
			if pr.URL != "" && pr.URL != model.ChangeRequestURL(repoOf(pr.Forge, pr.Host, pr.Repo), pr.Number) {
				t.Errorf("card %s: pull request link %q is not the built one", c.ID, pr.URL)
			}
			if pr.Forge == ForgeGitLab && !slices.Contains(s.GitLabHosts, pr.Host) {
				t.Errorf("card %s: GitLab host %q not in gitlabHosts", c.ID, pr.Host)
			}
		}
		for _, is := range c.Issues {
			if is.URL != "" && is.URL != model.ForgeIssueURL(repoOf(is.Forge, is.Host, is.Repo), is.Number) {
				t.Errorf("card %s: issue link %q is not the built one", c.ID, is.URL)
			}
		}
	}
	if !forges[ForgeGitHub] || !forges[ForgeGitLab] {
		t.Errorf("golden needs a pull request on each forge, has %v", forges)
	}
}
