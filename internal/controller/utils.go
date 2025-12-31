package controller

import (
	"fmt"
	"strings"
)

func slugDNS(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	repl := []string{"@", "-", ".", "-", "_", "-", ":", "-"}
	r := strings.NewReplacer(repl...)
	s = r.Replace(s)

	// tieni solo [a-z0-9-]
	out := make([]rune, 0, len(s))
	lastDash := false
	for _, ch := range s {
		isAZ := ch >= 'a' && ch <= 'z'
		is09 := ch >= '0' && ch <= '9'
		if isAZ || is09 {
			out = append(out, ch)
			lastDash = false
			continue
		}
		if ch == '-' && !lastDash {
			out = append(out, '-')
			lastDash = true
		}
	}
	res := strings.Trim(string(out), "-")
	if res == "" {
		return "x"
	}
	return res
}

func deriveNamespace(owner, projectName string) string {
	return fmt.Sprintf("%s-%s", slugDNS(owner), slugDNS(projectName))
}

func deriveGroupBase(owner, projectName string) string {
	return fmt.Sprintf("s4t:%s-%s", slugDNS(owner), slugDNS(projectName))
}

func extractUsernameFromOIDC(oidc string) string {
	oidc = strings.TrimSpace(oidc)
	parts := strings.Split(oidc, "#")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return oidc
}
