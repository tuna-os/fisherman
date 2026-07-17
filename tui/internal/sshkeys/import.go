package sshkeys

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 10 * time.Second}

// FetchGitHub fetches public SSH keys for a GitHub username.
func FetchGitHub(username string) ([]string, error) {
	return fetch(fmt.Sprintf("https://github.com/%s.keys", username))
}

// FetchGitLab fetches public SSH keys for a GitLab username.
func FetchGitLab(username string) ([]string, error) {
	return fetch(fmt.Sprintf("https://gitlab.com/%s.keys", username))
}

func fetch(url string) ([]string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("user not found")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var keys []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			keys = append(keys, line)
		}
	}
	return keys, nil
}
