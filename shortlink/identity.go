// Package shortlink defines the source identity shared by API, storage, and
// command-line callers. A source is a canonical domain and escaped URI path.
package shortlink

import (
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

var (
	ErrInvalidDomain = errors.New("invalid source domain")
	ErrInvalidPath   = errors.New("invalid source path")
	ErrInvalidName   = errors.New("invalid short link resource name")
)

// CanonicalDomain converts a valid DNS domain to lowercase ASCII. It accepts
// single-label names for local installations but never accepts an explicit port.
func CanonicalDomain(domain string) (string, error) {
	if domain == "" || strings.ContainsAny(domain, ":/?#@") {
		return "", ErrInvalidDomain
	}
	value, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDomain, err)
	}
	value = strings.ToLower(value)
	if len(value) > 253 {
		return "", ErrInvalidDomain
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalidDomain
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !isAlphaNumeric(c) && c != '-' {
				return "", ErrInvalidDomain
			}
		}
	}
	return value, nil
}

// CanonicalPath retains URI path identity. Literal slashes, plus signs, case,
// and repeated slashes remain distinct; percent escapes of reserved bytes stay
// escaped with uppercase hex, while escapes of unreserved bytes are decoded.
func CanonicalPath(path string) (string, error) {
	if !utf8.ValidString(path) || !strings.HasPrefix(path, "/") {
		return "", ErrInvalidPath
	}
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '%' {
			if i+2 >= len(path) {
				return "", ErrInvalidPath
			}
			hi, lo := hexValue(path[i+1]), hexValue(path[i+2])
			if hi < 0 || lo < 0 {
				return "", ErrInvalidPath
			}
			decoded := byte(hi<<4 | lo)
			if isUnreserved(decoded) {
				out.WriteByte(decoded)
			} else {
				out.WriteByte('%')
				out.WriteByte(hex[hi])
				out.WriteByte(hex[lo])
			}
			i += 2
			continue
		}
		if c == '?' || c == '#' || c == '\\' || c == 0 {
			return "", ErrInvalidPath
		}
		if isPathByte(c) {
			out.WriteByte(c)
		} else {
			out.WriteByte('%')
			out.WriteByte(hex[c>>4])
			out.WriteByte(hex[c&15])
		}
	}
	return out.String(), nil
}

// ResourceName derives the only resource name for a source address. The last
// segment is p followed by lowercase, unpadded RFC 4648 Base32 of the
// canonical escaped path. An explicit "/" maps to "pf4".
func ResourceName(domain, path string) (string, error) {
	domain, err := CanonicalDomain(domain)
	if err != nil {
		return "", err
	}
	path, err = CanonicalPath(path)
	if err != nil {
		return "", err
	}
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(path)))
	return "domains/" + domain + "/shortLinks/p" + encoded, nil
}

// ParseResourceName verifies a canonical name and reverses its encoded path.
func ParseResourceName(name string) (domain, path string, err error) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "domains" || parts[2] != "shortLinks" ||
		!strings.HasPrefix(parts[3], "p") || len(parts[3]) < 2 {
		return "", "", ErrInvalidName
	}
	pathBytes, decodeErr := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(parts[3][1:]))
	if decodeErr != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidName, decodeErr)
	}
	nameAgain, nameErr := ResourceName(parts[1], string(pathBytes))
	if nameErr != nil || nameAgain != name {
		return "", "", ErrInvalidName
	}
	return parts[1], string(pathBytes), nil
}

func isAlphaNumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

func isUnreserved(c byte) bool {
	return isAlphaNumeric(c) || c >= 'A' && c <= 'Z' || strings.ContainsRune("-._~", rune(c))
}

func isPathByte(c byte) bool {
	return isUnreserved(c) || strings.ContainsRune("!$&'()*+,;=:@/", rune(c))
}

func hexValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}
