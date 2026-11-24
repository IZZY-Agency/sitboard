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
// "scheme://[user@]host[:port]/path(.git)". An https or http address is a
// web one: its port stays on the host ("host:8443", model.ValidGitLabSite)
// and its path is as the web addresses carry it, a GitLab subfolder
// included; an https address's own port 443, written out, is dropped (the
// same address without it). An ssh, git or scp-like address is not (bare): its port is the
// git one and is dropped, and its path never has the subfolder in front
// (the core puts it there, as it knows glab's setting for the host).
func ParseRemote(raw string) (host, path string, bare, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return "", "", false, false
	}
	if !strings.Contains(raw, "://") {
		// scp-like: [user@]host:path, where the host part has no "/".
		before, after, found := strings.Cut(raw, ":")
		if !found || strings.Contains(before, "/") {
			return "", "", false, false
		}
		if i := strings.LastIndex(before, "@"); i >= 0 {
			before = before[i+1:]
		}
		host, path, bare = strings.ToLower(before), after, true
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
			return "", "", false, false
		}
		host = strings.ToLower(u.Hostname())
		switch u.Scheme {
		case "ssh", "git":
			bare = true
		case "https", "http":
			if port := u.Port(); port != "" && (port != "443" || u.Scheme != "https") { // https's own port, written out, is no port
				if !model.ValidWebPort(port) {
					return "", "", false, false
				}
				host += ":" + port
			} else if strings.HasSuffix(u.Host, ":") {
				return "", "", false, false // "host:" with no port
			}
		default:
			return "", "", false, false
		}
		path = u.Path
	}
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if !model.ValidGitLabSite(host) || !ValidPath(path) {
		return "", "", false, false
	}
	return host, path, bare, true
}
