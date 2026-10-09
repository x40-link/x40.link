package shortlink_test

import (
	"testing"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/stretchr/testify/require"
)

func TestCanonicalDomain(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		valid bool
	}{
		{"EXAMPLE.COM", "example.com", true},
		{"localhost", "localhost", true},
		{"exämple.com", "xn--exmple-cua.com", true},
		{"example.com:443", "", false},
		{"https://example.com", "", false},
		{"bad..example", "", false},
		{"_internal.example", "", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := shortlink.CanonicalDomain(tc.input)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCanonicalPath(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		valid bool
	}{
		{"/", "/", true},
		{"/foo/bar", "/foo/bar", true},
		{"/foo+bar", "/foo+bar", true},
		{"/foo//bar", "/foo//bar", true},
		{"/Foo", "/Foo", true},
		{"/foo%2fbar", "/foo%2Fbar", true},
		{"/foo%7ebar", "/foo~bar", true},
		{"/café", "/caf%C3%A9", true},
		{"", "", false},
		{"foo", "", false},
		{"/bad%2", "", false},
		{"/path?query", "", false},
		{"/path#fragment", "", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := shortlink.CanonicalPath(tc.input)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestResourceNameRoundTrip(t *testing.T) {
	paths := []string{"/", "/foo/bar", "/foo+bar", "/foo%2Fbar", "/foo//bar", "/Foo"}
	names := map[string]bool{}
	for _, path := range paths {
		name, err := shortlink.ResourceName("EXAMPLE.COM", path)
		require.NoError(t, err)
		require.False(t, names[name], "distinct source paths share a name: %s", name)
		names[name] = true
		domain, decodedPath, err := shortlink.ParseResourceName(name)
		require.NoError(t, err)
		require.Equal(t, "example.com", domain)
		require.Equal(t, path, decodedPath)
	}

	root, err := shortlink.ResourceName("example.com", "/")
	require.NoError(t, err)
	require.Equal(t, "domains/example.com/shortLinks/pf4", root)

	for _, name := range []string{
		"domains/example.com/shortLinks/PF4",
		"domains/example.com/shortLinks/pf4=",
		"domains/example.com/shortLinks/pbad",
		"domains/example.com/shortLinks/pf4/other",
		"domains/example.com:443/shortLinks/pf4",
	} {
		_, _, err := shortlink.ParseResourceName(name)
		require.Error(t, err, name)
	}
}
