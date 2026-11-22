package gitlab

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Caps per GraphQL call; a lookup asking for more makes more calls. GitLab refuses a query whose complexity is over 200
// (anonymous) or 250 (logged in); the query below with both lists at 50
// scored 148 on gitlab.com (testdata/fixtures/glab/1.131.0/README.md).
const (
	maxIIDs     = 50
	maxBranches = 50
	branchFirst = 50 // newest merge requests over all the branches asked for
)

// mrFields are the fields read for each merge request. The address is not
// asked for: it is built from the host, the path and the number.
const mrFields = `iid title sourceBranch sourceProjectId projectId state draft createdAt closedAt mergedAt headPipeline{status}`

// approvalFields are asked only of an instance that has them: Community
// Edition has no approvalsLeft or approvalsRequired and refuses the whole
// query when they are asked for.
const approvalFields = ` approved approvalsLeft approvalsRequired`

// buildQuery writes the GraphQL query for one project. Only numbers this
// function formats itself go into the text; the project path and branch names
// are variables ($path, $b0, $b1…), sent with glab's -f, which sends a value
// as it is (no "@file", no ":placeholder").
func buildQuery(iids []int, nBranches int, approvals bool) string {
	var q strings.Builder
	q.WriteString("query($path:ID!")
	for i := range nBranches {
		fmt.Fprintf(&q, ",$b%d:String!", i)
	}
	q.WriteString("){project(fullPath:$path){")
	if len(iids) > 0 {
		q.WriteString("m:mergeRequests(iids:[")
		for i, n := range iids {
			if i > 0 {
				q.WriteByte(',')
			}
			q.WriteByte('"')
			q.WriteString(strconv.Itoa(n))
			q.WriteByte('"')
		}
		fmt.Fprintf(&q, "],first:%d){nodes{...F}} ", len(iids))
	}
	if nBranches > 0 {
		q.WriteString("b:mergeRequests(sourceBranches:[")
		for i := range nBranches {
			if i > 0 {
				q.WriteByte(',')
			}
			fmt.Fprintf(&q, "$b%d", i)
		}
		fmt.Fprintf(&q, "],first:%d,sort:CREATED_DESC){nodes{...F}} ", branchFirst)
	}
	q.WriteString("}} fragment F on MergeRequest{" + mrFields)
	if approvals {
		q.WriteString(approvalFields)
	}
	q.WriteString("}")
	return q.String()
}

type glMR struct {
	IID               string  `json:"iid"`
	Title             string  `json:"title"`
	SourceBranch      string  `json:"sourceBranch"`
	SourceProjectID   *int    `json:"sourceProjectId"`
	ProjectID         int     `json:"projectId"`
	State             string  `json:"state"`
	Draft             bool    `json:"draft"`
	CreatedAt         string  `json:"createdAt"`
	ClosedAt          *string `json:"closedAt"`
	MergedAt          *string `json:"mergedAt"`
	Approved          *bool   `json:"approved"`
	ApprovalsLeft     *int    `json:"approvalsLeft"`
	ApprovalsRequired *int    `json:"approvalsRequired"`
	HeadPipeline      *struct {
		Status string `json:"status"`
	} `json:"headPipeline"`
}

type conn struct {
	Nodes []*glMR `json:"nodes"`
}

type answer struct {
	Errors []struct {
		Message string `json:"message"`
		Path    []any  `json:"path"`
	} `json:"errors"`
	Data *struct {
		Project *struct {
			M *conn `json:"m"`
			B *conn `json:"b"`
		} `json:"project"`
	} `json:"data"`
}

var errNoProject = errors.New("gitlab: project not found")

// approvalFieldNames are the fields of approvalFields.
var approvalFieldNames = strings.Fields(approvalFields)

// lacksApprovalFields says whether GitLab refused a query only because it
// has no approval fields (Community Edition): every error is graphql-ruby's
// undefinedField on MergeRequest for one of them. The extensions are read,
// not the message, which can carry a spelling suggestion.
func lacksApprovalFields(out []byte) bool {
	var a struct {
		Errors []struct {
			Extensions struct {
				Code      string `json:"code"`
				TypeName  string `json:"typeName"`
				FieldName string `json:"fieldName"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(out, &a) != nil || len(a.Errors) == 0 {
		return false
	}
	for _, e := range a.Errors {
		x := e.Extensions
		if x.Code != "undefinedField" || x.TypeName != "MergeRequest" || !slices.Contains(approvalFieldNames, x.FieldName) {
			return false
		}
	}
	return true
}

// found is one merge request from the answer with the project id its
// closing issues are asked under.
type found struct {
	mr        MergeRequest
	projectID int
}

// shape is what a query asked for.
type shape struct {
	iids, branches, approvals bool
}

// parsed is one GraphQL answer read.
type parsed struct {
	mrs        []found
	branchFull bool // the branch list came back full: older ones may be missing
}

var errIncomplete = errors.New("gitlab: GitLab answered only part of the lookup")

// parseAnswer reads a GraphQL answer. A project that is gone or not visible
// to the account answers null without errors: errNoProject. Merge requests
// found by number come first and win over the same one found by branch; a
// merge request from a fork is kept only when asked for by number (any fork
// can have a branch called main) and says it is from a fork.
//
// GitLab can answer with data and errors. An error on one merge request's
// pipeline or approval fields makes that value unknown; any other error, or
// a list that was asked for and is missing, makes the whole answer
// errIncomplete: a merge request left out would read as one that does not
// exist.
func parseAnswer(out []byte, host, path string, q shape) (parsed, error) {
	var a answer
	if err := json.Unmarshal(out, &a); err != nil || a.Data == nil {
		return parsed{}, errors.New("gitlab: unexpected answer from glab")
	}
	if a.Data.Project == nil {
		if len(a.Errors) > 0 {
			return parsed{}, errIncomplete
		}
		return parsed{}, errNoProject
	}
	type node struct {
		conn string
		i    int
	}
	noChecks, noApproval := map[node]bool{}, map[node]bool{}
	for _, e := range a.Errors {
		// ["project", "m"|"b", "nodes", i, field, …]
		if len(e.Path) < 5 || e.Path[0] != "project" || e.Path[2] != "nodes" {
			return parsed{}, errIncomplete
		}
		conn, _ := e.Path[1].(string)
		i, isNum := e.Path[3].(float64)
		if !isNum || (conn != "m" && conn != "b") {
			return parsed{}, errIncomplete
		}
		switch e.Path[4] {
		case "headPipeline":
			noChecks[node{conn, int(i)}] = true
		case "approved", "approvalsLeft", "approvalsRequired":
			noApproval[node{conn, int(i)}] = true
		default:
			return parsed{}, errIncomplete
		}
	}
	pr := a.Data.Project
	if (q.iids && pr.M == nil) || (q.branches && pr.B == nil) {
		return parsed{}, errIncomplete
	}
	// A field error that nulled its whole node or list (approved is non-null)
	// leaves a merge request out: it would read as absent.
	for _, errored := range []map[node]bool{noChecks, noApproval} {
		for n := range errored {
			c := pr.M
			if n.conn == "b" {
				c = pr.B
			}
			if c == nil || n.i < 0 || n.i >= len(c.Nodes) || c.Nodes[n.i] == nil {
				return parsed{}, errIncomplete
			}
		}
	}
	seen := map[int]bool{}
	var res parsed
	add := func(g *glMR, conn string, i int) {
		if g == nil {
			return
		}
		fork := g.SourceProjectID == nil || *g.SourceProjectID != g.ProjectID
		if conn == "b" && fork {
			return
		}
		mr, ok := toMR(g, host, path, q.approvals)
		if !ok || seen[mr.IID] {
			return
		}
		mr.FromFork = fork
		if noChecks[node{conn, i}] {
			mr.Checks = model.ChecksUnknown
		}
		if noApproval[node{conn, i}] {
			mr.Approval = ApprovalUnknown
		}
		seen[mr.IID] = true
		res.mrs = append(res.mrs, found{mr: mr, projectID: g.ProjectID})
	}
	if pr.M != nil {
		for i, g := range pr.M.Nodes {
			add(g, "m", i)
		}
	}
	if pr.B != nil {
		for i, g := range pr.B.Nodes {
			add(g, "b", i)
		}
		res.branchFull = len(pr.B.Nodes) >= branchFirst
	}
	slices.SortFunc(res.mrs, func(x, y found) int { return x.mr.IID - y.mr.IID })
	return res, nil
}

func toMR(g *glMR, host, path string, approvals bool) (MergeRequest, bool) {
	iid, err := strconv.Atoi(g.IID)
	if err != nil || iid <= 0 {
		return MergeRequest{}, false
	}
	state, ok := mapState(g.State)
	if !ok {
		return MergeRequest{}, false
	}
	mr := MergeRequest{
		Host: host, Path: path, IID: iid, Title: g.Title, URL: MergeRequestURL(host, path, iid),
		SourceBranch: g.SourceBranch, State: state, Draft: g.Draft, OpenedAt: parseTime(g.CreatedAt),
		Checks:   mapPipeline(pipelineStatus(g), g.HeadPipeline != nil),
		Approval: ApprovalUnknown,
	}
	if approvals {
		mr.Approval = mapApproval(g.Approved, g.ApprovalsLeft, g.ApprovalsRequired)
	}
	if state == model.PRStateMerged && g.MergedAt != nil {
		mr.ClosedAt = parseTime(*g.MergedAt)
	}
	if mr.ClosedAt.IsZero() && state != model.PRStateOpen && g.ClosedAt != nil {
		mr.ClosedAt = parseTime(*g.ClosedAt)
	}
	return mr, true
}

func pipelineStatus(g *glMR) string {
	if g.HeadPipeline == nil {
		return ""
	}
	return g.HeadPipeline.Status
}

// closingPerPage is GitLab's largest page; closingMaxPages bounds the pages
// asked for one merge request.
const (
	closingPerPage  = 100
	closingMaxPages = 5
)

// closesIssuesPath is the REST address of one page of the issues a merge
// request closes. Every part is a number this adapter formats.
func closesIssuesPath(projectID, iid, page int) string {
	return "projects/" + strconv.Itoa(projectID) + "/merge_requests/" + strconv.Itoa(iid) +
		"/closes_issues?per_page=" + strconv.Itoa(closingPerPage) + "&page=" + strconv.Itoa(page)
}

// parseClosing reads one page of a closes_issues answer: the issues of the
// same project, in order, without repeats, and how many items the page held
// (a full page means there may be another).
func parseClosing(out []byte, projectID int) ([]int, int, error) {
	var items []struct {
		IID       int `json:"iid"`
		ProjectID int `json:"project_id"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, 0, errors.New("gitlab: unexpected closes_issues answer")
	}
	nums := []int{}
	for _, it := range items {
		if it.ProjectID == projectID && it.IID > 0 && !slices.Contains(nums, it.IID) {
			nums = append(nums, it.IID)
		}
	}
	return nums, len(items), nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func cleanIIDs(in []int) []int {
	var out []int
	for _, n := range in {
		if ValidIID(n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func cleanBranches(in []string) []string {
	var out []string
	for _, b := range in {
		if b == "" || len(b) > 255 || strings.ContainsAny(b, "\x00\n\r") {
			continue
		}
		out = append(out, b)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
