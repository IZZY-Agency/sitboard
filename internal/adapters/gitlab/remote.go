package gitlab

import (
	"net/url"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// GitLabCom is the one GitLab host recognised by name; whether it, or any
// other host, may be asked about is Provider.Known's answer: only a host glab
// is logged in to.
const GitLabCom = model.GitLabCom

// ValidHost reports whether h is a lowercase DNS name with at least one dot
// and no port, user or path: the only form passed to glab's --hostname.
func ValidHost(h string) bool { return model.ValidGitLabHost(h) }

// ValidPath reports whether p is a GitLab project path: model.ValidGitLabPath.
func ValidPath(p string) bool { return model.ValidGitLabPath(p) }

// ParseRemote returns the host and project path of a git remote URL, or
// ok=false when the URL is not one this adapter can name a project from. It
// says nothing about whether the host is GitLab: that is Provider.Known.
//
// Accepted: scp-like "user@host:path(.git)", and ssh, https, http or git URLs
// "scheme://[user@]host[:port]/path(.git)". An ssh port is dropped (it is the
// git port, not the web one); an https or http URL with a port is refused,
// because the port cannot be told apart from a different instance.
func ParseRemote(raw string) (host, path string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return "", "", false
	}
	if !strings.Contains(raw, "://") {
		// scp-like: [user@]host:path, where the host part has no "/".
		before, after, found := strings.Cut(raw, ":")
		if !found || strings.Contains(before, "/") {
			return "", "", false
		}
		if i := strings.LastIndex(before, "@"); i >= 0 {
			before = before[i+1:]
		}
		host, path = strings.ToLower(before), after
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
			return "", "", false
		}
		switch u.Scheme {
		case "ssh", "git":
		case "https", "http":
			if u.Port() != "" {
				return "", "", false
			}
		default:
			return "", "", false
		}
		host, path = strings.ToLower(u.Hostname()), u.Path
	}
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if !ValidHost(host) || !ValidPath(path) {
		return "", "", false
	}
	return host, path, true
}
