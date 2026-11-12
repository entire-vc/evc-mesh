package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeRepositoryRefs_CanonicalForm(t *testing.T) {
	got, err := NormalizeRepositoryRefs([]RepositoryRef{
		{Provider: " GitLab ", Host: "https://Git.Example.org:443/", ProjectPath: " entire-vc/evc-mesh.git/ "},
		{Provider: "github", Host: "http://github.com:80", ProjectPath: "Entire-VC/Evc-Mesh"},
		{Provider: "gitlab", Host: "git.example.org:443", ProjectPath: "group/sub/project//"},
		{Provider: "gitlab", Host: "git.example.org:8443", ProjectPath: "entire-vc/evc-mesh"},
		{Provider: "gitlab", Host: "git.example.org:65535", ProjectPath: "entire-vc/evc-mesh"},
	})
	require.NoError(t, err)
	require.Equal(t, []RepositoryRef{
		{Provider: "gitlab", Host: "git.example.org", ProjectPath: "entire-vc/evc-mesh"},
		// project_path keeps its case: only the host is case-insensitive.
		{Provider: "github", Host: "github.com", ProjectPath: "Entire-VC/Evc-Mesh"},
		{Provider: "gitlab", Host: "git.example.org", ProjectPath: "group/sub/project"},
		// a non-default port is part of the host identity.
		{Provider: "gitlab", Host: "git.example.org:8443", ProjectPath: "entire-vc/evc-mesh"},
		// the highest valid port is still a port.
		{Provider: "gitlab", Host: "git.example.org:65535", ProjectPath: "entire-vc/evc-mesh"},
	}, got)
}

func TestNormalizeRepositoryRefs_EmptyListIsConfiguredDenyAll(t *testing.T) {
	got, err := NormalizeRepositoryRefs([]RepositoryRef{})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)

	_, err = NormalizeRepositoryRefs(nil)
	require.ErrorContains(t, err, "must be an array")
}

func TestNormalizeRepositoryRefs_DuplicateAfterNormalizationRefusesList(t *testing.T) {
	_, err := NormalizeRepositoryRefs([]RepositoryRef{
		{Provider: "gitlab", Host: "git.example.org", ProjectPath: "entire-vc/evc-mesh"},
		{Provider: "GITLAB", Host: "https://GIT.example.org/", ProjectPath: "entire-vc/evc-mesh.git"},
	})
	require.ErrorContains(t, err, "repositories[1] duplicates repositories[0]")

	// The same project path on another host or provider is a different repository.
	_, err = NormalizeRepositoryRefs([]RepositoryRef{
		{Provider: "gitlab", Host: "git.example.org", ProjectPath: "entire-vc/evc-mesh"},
		{Provider: "gitlab", Host: "gitlab.com", ProjectPath: "entire-vc/evc-mesh"},
		{Provider: "github", Host: "git.example.org", ProjectPath: "entire-vc/evc-mesh"},
	})
	require.NoError(t, err)
}

func TestNormalizeRepositoryRefs_MalformedIsRefused(t *testing.T) {
	valid := RepositoryRef{Provider: "gitlab", Host: "git.example.org", ProjectPath: "entire-vc/evc-mesh"}
	cases := map[string]RepositoryRef{
		"unknown provider":  {Provider: "bitbucket", Host: valid.Host, ProjectPath: valid.ProjectPath},
		"empty provider":    {Provider: "", Host: valid.Host, ProjectPath: valid.ProjectPath},
		"empty host":        {Provider: "gitlab", Host: " ", ProjectPath: valid.ProjectPath},
		"ssh scheme":        {Provider: "gitlab", Host: "ssh://git.example.org", ProjectPath: valid.ProjectPath},
		"scp-like remote":   {Provider: "gitlab", Host: "git@git.example.org", ProjectPath: valid.ProjectPath},
		"user info":         {Provider: "gitlab", Host: "https://user@git.example.org", ProjectPath: valid.ProjectPath},
		"host with path":    {Provider: "gitlab", Host: "https://git.example.org/entire-vc", ProjectPath: valid.ProjectPath},
		"host with query":   {Provider: "gitlab", Host: "git.example.org?x=1", ProjectPath: valid.ProjectPath},
		"host inner space":  {Provider: "gitlab", Host: "git example.org", ProjectPath: valid.ProjectPath},
		"host empty label":  {Provider: "gitlab", Host: "git..host", ProjectPath: valid.ProjectPath},
		"port above 65535":  {Provider: "gitlab", Host: "git.example.org:65536", ProjectPath: valid.ProjectPath},
		"port 99999":        {Provider: "gitlab", Host: "git.example.org:99999", ProjectPath: valid.ProjectPath},
		"port zero":         {Provider: "gitlab", Host: "git.example.org:0", ProjectPath: valid.ProjectPath},
		"port leading zero": {Provider: "gitlab", Host: "git.example.org:0443", ProjectPath: valid.ProjectPath},
		"empty path":        {Provider: "gitlab", Host: valid.Host, ProjectPath: ""},
		"only .git":         {Provider: "gitlab", Host: valid.Host, ProjectPath: ".git"},
		"single segment":    {Provider: "gitlab", Host: valid.Host, ProjectPath: "evc-mesh"},
		"leading slash":     {Provider: "gitlab", Host: valid.Host, ProjectPath: "/entire-vc/evc-mesh"},
		"empty segment":     {Provider: "gitlab", Host: valid.Host, ProjectPath: "entire-vc//evc-mesh"},
		"dot-dot segment":   {Provider: "gitlab", Host: valid.Host, ProjectPath: "entire-vc/../evc-mesh"},
		"full URL as path":  {Provider: "gitlab", Host: valid.Host, ProjectPath: "https://git.example.org/entire-vc/evc-mesh"},
		"space in path":     {Provider: "gitlab", Host: valid.Host, ProjectPath: "entire vc/evc-mesh"},
		"query in path":     {Provider: "gitlab", Host: valid.Host, ProjectPath: "entire-vc/evc-mesh?ref=main"},
		"overlong host":     {Provider: "gitlab", Host: strings.Repeat("a", 254), ProjectPath: valid.ProjectPath},
		"empty after .git/": {Provider: "gitlab", Host: valid.Host, ProjectPath: "entire-vc/.git/"},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			// A bad entry refuses the whole list, valid neighbours included.
			_, err := NormalizeRepositoryRefs([]RepositoryRef{valid, ref})
			require.Error(t, err)
			require.Contains(t, err.Error(), "repositories[1]")
		})
	}

	// Positive control: the same valid entry alone passes.
	_, err := NormalizeRepositoryRefs([]RepositoryRef{valid})
	require.NoError(t, err)
}

func TestNormalizeRepositoryRefs_ListSizeBound(t *testing.T) {
	refs := make([]RepositoryRef, MaxAgentRepositories+1)
	for i := range refs {
		refs[i] = RepositoryRef{Provider: "gitlab", Host: "git.example.org", ProjectPath: "g/p" + strings.Repeat("x", i)}
	}
	_, err := NormalizeRepositoryRefs(refs)
	require.ErrorContains(t, err, "at most")
	_, err = NormalizeRepositoryRefs(refs[:MaxAgentRepositories])
	require.NoError(t, err)
}
