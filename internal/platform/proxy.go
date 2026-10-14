package platform

import (
	"net/url"
	"strings"
)

// parseProxy accepts "host:port" as well as full URLs.
func parseProxy(p string) (*url.URL, error) {
	p = strings.TrimSpace(p)
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	return url.Parse(p)
}

// ProxyURL normalises a user-entered proxy ("127.0.0.1:7890") into a URL string, or "" if empty.
func ProxyURL(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	u, err := parseProxy(p)
	if err != nil {
		return ""
	}
	return u.String()
}
