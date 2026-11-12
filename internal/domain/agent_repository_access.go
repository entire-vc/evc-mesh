package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// AgentRepositoryAccessChangedEvent is the agent_activity_log event_type of a
// repository-access change. Only the repository-access write path creates it;
// POST /agents/{id}/activity refuses it so the audit trail cannot be forged.
const AgentRepositoryAccessChangedEvent = "repository_access_changed"

// MaxAgentRepositories bounds one agent's list; a runner works in a handful.
const MaxAgentRepositories = 200

// RepositoryRef is the canonical identity of one repository: the triple, not the
// bare project path, so a same-named project on another host never matches.
type RepositoryRef struct {
	Provider    string `json:"provider"`
	Host        string `json:"host"`
	ProjectPath string `json:"project_path"`
}

// AgentRepositoryAccess is the configured list and its revision. A nil
// *AgentRepositoryAccess means "not configured" (the runner's legacy list
// applies); a non-nil value with an empty Repositories means "no repository".
type AgentRepositoryAccess struct {
	Revision     int64           `json:"revision"`
	Repositories []RepositoryRef `json:"repositories"`
}

// RepositoryProviders are the providers a reference may name.
var RepositoryProviders = map[string]bool{"gitlab": true, "github": true}

var (
	repositoryHostRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[1-9][0-9]{0,4})?$`)
	repositorySegmentRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
)

// NormalizeRepositoryRefs returns the canonical form of a list or the reason it
// is refused. The policy is strict; there is no tolerant fallback:
//
//   - provider: trimmed, lowercased, one of RepositoryProviders;
//   - host: trimmed, lowercased; an http:// or https:// prefix and one trailing
//     "/" are removed, as is the default port of the scheme (":443" for https
//     and a bare host, ":80" for http); any other scheme, user info, path,
//     query or non-host character is refused; another port is kept;
//   - project_path: trimmed; trailing "/" then one ".git" suffix are removed;
//     it must then be two or more non-empty segments of [A-Za-z0-9_.-], none
//     "." or "..", without a leading "/"; case is kept;
//   - a triple that occurs twice after normalization refuses the whole list.
//
// Order is preserved. nil input is refused: "not configured" is not writable.
func NormalizeRepositoryRefs(in []RepositoryRef) ([]RepositoryRef, error) {
	if in == nil {
		return nil, fmt.Errorf("repositories must be an array")
	}
	if len(in) > MaxAgentRepositories {
		return nil, fmt.Errorf("at most %d repositories", MaxAgentRepositories)
	}
	out := make([]RepositoryRef, 0, len(in))
	seen := make(map[RepositoryRef]int, len(in))
	for i, ref := range in {
		n, err := normalizeRepositoryRef(ref)
		if err != nil {
			return nil, fmt.Errorf("repositories[%d]: %w", i, err)
		}
		if first, dup := seen[n]; dup {
			return nil, fmt.Errorf("repositories[%d] duplicates repositories[%d] (%s %s/%s)", i, first, n.Provider, n.Host, n.ProjectPath)
		}
		seen[n] = i
		out = append(out, n)
	}
	return out, nil
}

func normalizeRepositoryRef(ref RepositoryRef) (RepositoryRef, error) {
	provider := strings.ToLower(strings.TrimSpace(ref.Provider))
	if !RepositoryProviders[provider] {
		return RepositoryRef{}, fmt.Errorf("provider must be gitlab or github")
	}
	host, err := normalizeRepositoryHost(ref.Host)
	if err != nil {
		return RepositoryRef{}, err
	}
	path, err := normalizeRepositoryPath(ref.ProjectPath)
	if err != nil {
		return RepositoryRef{}, err
	}
	return RepositoryRef{Provider: provider, Host: host, ProjectPath: path}, nil
}

func normalizeRepositoryHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	defaultPorts := []string{":443"}
	switch {
	case strings.HasPrefix(host, "https://"):
		host = strings.TrimPrefix(host, "https://")
	case strings.HasPrefix(host, "http://"):
		host = strings.TrimPrefix(host, "http://")
		defaultPorts = []string{":80"}
	case strings.Contains(host, "://"):
		return "", fmt.Errorf("host scheme must be http or https")
	}
	host = strings.TrimSuffix(host, "/")
	for _, port := range defaultPorts {
		host = strings.TrimSuffix(host, port)
	}
	if host == "" || len(host) > 253 || !repositoryHostRe.MatchString(host) {
		return "", fmt.Errorf("host must be a host name with an optional port")
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		if port, err := strconv.Atoi(host[i+1:]); err != nil || port > 65535 {
			return "", fmt.Errorf("host port must be between 1 and 65535")
		}
	}
	return host, nil
}

func normalizeRepositoryPath(raw string) (string, error) {
	path := strings.TrimRight(strings.TrimSpace(raw), "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" || len(path) > 512 || strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("project_path must be group/project")
	}
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return "", fmt.Errorf("project_path must be group/project")
	}
	for _, s := range segments {
		if s == "." || s == ".." || !repositorySegmentRe.MatchString(s) {
			return "", fmt.Errorf("project_path segment %q is not allowed", s)
		}
	}
	return path, nil
}
