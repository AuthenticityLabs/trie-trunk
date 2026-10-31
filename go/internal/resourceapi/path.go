// Package resourceapi shares public route recognition between the server and
// desktop proxy without linking storage or codec implementations into the shell.
package resourceapi

import "strings"

func IsPath(path string) bool {
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(p) != 2 || p[0] == "" || len(p[0]) > 128 {
		return false
	}
	for i, c := range p[0] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_' || c == '.')) {
			return false
		}
	}
	switch p[1] {
	case "registries", "collections", "seeds", "summaries", "timelines", "indexes":
		return true
	}
	return false
}
