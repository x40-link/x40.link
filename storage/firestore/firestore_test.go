package firestore

import (
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/andrewhowdencom/x40.link/storage"
)

func TestURLToPathDistinct(t *testing.T) {
	tests := []struct {
		left  string
		right string
	}{
		{"/foo/bar", "/foo+bar"},
		{"/foo/bar", "/foo%2Fbar"},
		{"/foo/bar", "/foo//bar"},
		{"/Foo", "/foo"},
		{"/", "/%2F"},
	}
	for _, tt := range tests {
		t.Run(tt.left+" vs "+tt.right, func(t *testing.T) {
			left, err := url.Parse("https://example.com" + tt.left)
			if err != nil {
				t.Fatal(err)
			}
			right, err := url.Parse("https://example.com" + tt.right)
			if err != nil {
				t.Fatal(err)
			}
			if urlToPath(left) == urlToPath(right) {
				t.Fatalf("%q and %q share a storage key", tt.left, tt.right)
			}
		})
	}
}

func TestURLToPathEncodingAndLimit(t *testing.T) {
	from, err := url.Parse("https://example.com/foo%2Fbar")
	if err != nil {
		t.Fatal(err)
	}
	key := urlToPath(from)
	id := key[strings.LastIndex(key, "/")+1:]
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimPrefix(id, "p-")))
	if err != nil || string(decoded) != "/foo%2Fbar" {
		t.Fatalf("encoded key does not preserve the escaped path: %q, %v", decoded, err)
	}
	_, err = (Firestore{}).sourceRef(&url.URL{Host: "example.com", Path: "/" + strings.Repeat("x", 936)})
	if !errors.Is(err, storage.ErrInvalidSource) {
		t.Fatalf("oversized encoded key accepted: %v", err)
	}
}

func TestURLToPathCanonical(t *testing.T) {
	tests := []struct{ left, right string }{
		{"http://EXAMPLE.com/foo", "https://example.com/foo"},
		{"http://EXAMPLE.com:8443/foo", "https://example.com/foo"},
		{"https://example.com/foo%2fbar", "https://example.com/foo%2Fbar"},
		{"https://example.com", "https://example.com/"},
		{"https://example.com/foo?a=1", "https://example.com/foo?a=2"},
	}
	for _, tt := range tests {
		left, err := url.Parse(tt.left)
		if err != nil {
			t.Fatal(err)
		}
		right, err := url.Parse(tt.right)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := urlToPath(left), urlToPath(right); got != want {
			t.Errorf("keys differ for %q and %q: %q != %q", tt.left, tt.right, got, want)
		}
	}
}
