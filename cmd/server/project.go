package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
)

func makeID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// normalizeRepository accepts HTTPS, SSH URLs and scp-style SSH URLs.
// Credentials are discarded; host is case-folded and repository path preserved.
func normalizeRepository(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var host, path string
	if !strings.Contains(raw, "://") {
		// SCP syntax is [user@]host:path.
		if i := strings.Index(raw, ":"); i > 0 {
			left := raw[:i]
			if at := strings.LastIndex(left, "@"); at >= 0 {
				left = left[at+1:]
			}
			if !strings.Contains(left, "/") {
				host, path = left, raw[i+1:]
			}
		}
	}
	if host == "" {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", errors.New("repository must be an SSH or HTTPS URL")
		}
		if u.Scheme != "https" && u.Scheme != "ssh" {
			return "", errors.New("unsupported repository URL scheme")
		}
		host = u.Hostname()
		port := u.Port()
		if port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "ssh" && port == "22")) {
			host = net.JoinHostPort(host, port)
		}
		path = strings.Trim(u.EscapedPath(), "/")
	}
	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return "", errors.New("repository URL must include host and repository path")
	}
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return "", errors.New("repository path is empty")
	}
	return host + "/" + path, nil
}
