// Package firestore implements a storage layer with Google cloud firestore.
package firestore

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type document struct {
	From  string `firestore:"from"`
	To    string `firestore:"to"`
	Owner string `firestore:"owner"`
}

// FirestoreCollection contains source domains.
const FirestoreCollection = "links"

// PathCollection separates encoded path keys from the legacy "id" keys.
const PathCollection = "shortLinks"

// Firestore is the Google Cloud Firestore storage implementation.
type Firestore struct{ Client *firestore.Client }

// Get fetches a URL from storage.
func (fs Firestore) Get(ctx context.Context, from *url.URL) (*url.URL, error) {
	instrument(ctx)
	ref, err := fs.sourceRef(from)
	if err != nil {
		return nil, err
	}
	doc, err := fs.doc(ctx, ref)
	if status.Code(err) == codes.NotFound {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", storage.ErrFailed, err)
	}
	to, err := url.Parse(doc.To)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", storage.ErrCorrupt, err)
	}
	return to, nil
}

// Put atomically claims an address. Existing records are never overwritten.
func (fs Firestore) Put(ctx context.Context, from, to *url.URL) error {
	instrument(ctx)
	ref, err := fs.sourceRef(from)
	if err != nil {
		return err
	}
	agent, _ := ctx.Value(storage.CtxKeyAgent).(string)
	err = fs.Client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, getErr := tx.Get(ref)
		if status.Code(getErr) == codes.NotFound {
			return tx.Create(ref, document{From: canonicalSource(from).String(), To: to.String(), Owner: agent})
		}
		if getErr != nil {
			return getErr
		}
		var existing document
		if err := snap.DataTo(&existing); err != nil {
			return fmt.Errorf("%w: %s", storage.ErrCorrupt, err)
		}
		if existing.Owner != agent {
			return storage.ErrUnauthorized
		}
		return storage.ErrAlreadyExists
	})
	if errors.Is(err, storage.ErrAlreadyExists) || errors.Is(err, storage.ErrUnauthorized) || errors.Is(err, storage.ErrCorrupt) {
		return err
	}
	// The emulator can abort a losing concurrent create after exhausting
	// transaction retries. The committed winner still defines the correct
	// result for this caller, so classify it from the current document.
	if status.Code(err) == codes.Aborted {
		if snap, readErr := ref.Get(ctx); readErr == nil {
			var existing document
			if decodeErr := snap.DataTo(&existing); decodeErr != nil {
				return storage.ErrCorrupt
			}
			if existing.Owner != agent {
				return storage.ErrUnauthorized
			}
			return storage.ErrAlreadyExists
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %s", storage.ErrFailed, err)
	}
	return nil
}

// Owns reports whether the authenticated caller owns this address.
func (fs Firestore) Owns(ctx context.Context, from *url.URL) bool {
	instrument(ctx)
	agent, ok := ctx.Value(storage.CtxKeyAgent).(string)
	if !ok || agent == "" {
		return false
	}
	ref, err := fs.sourceRef(from)
	if err != nil {
		return false
	}
	doc, err := fs.doc(ctx, ref)
	return err == nil && doc.Owner == agent
}

// List returns links owned by the caller, optionally restricted to a domain.
func (fs Firestore) List(ctx context.Context, domain string) ([]storage.Link, error) {
	instrument(ctx)
	agent, ok := ctx.Value(storage.CtxKeyAgent).(string)
	if !ok || agent == "" {
		return nil, storage.ErrUnauthorized
	}
	var snaps []*firestore.DocumentSnapshot
	var err error
	if domain == "" {
		snaps, err = fs.Client.CollectionGroup(PathCollection).Where("owner", "==", agent).Documents(ctx).GetAll()
	} else {
		snaps, err = fs.Client.Collection(FirestoreCollection).Doc(strings.ToLower(domain)).Collection(PathCollection).Where("owner", "==", agent).Documents(ctx).GetAll()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", storage.ErrFailed, err)
	}
	links := make([]storage.Link, 0, len(snaps))
	for _, snap := range snaps {
		parent := snap.Ref.Parent.Parent
		if parent == nil || parent.Parent == nil || parent.Parent.ID != FirestoreCollection || parent.Parent.Parent != nil {
			continue
		}
		var doc document
		if err := snap.DataTo(&doc); err != nil {
			return nil, fmt.Errorf("%w: %s", storage.ErrCorrupt, err)
		}
		if doc.Owner != agent {
			continue
		}
		to, err := url.Parse(doc.To)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", storage.ErrCorrupt, err)
		}
		from, err := url.Parse(doc.From)
		if err != nil || doc.From == "" {
			return nil, storage.ErrCorrupt
		}
		links = append(links, storage.Link{From: from, To: to})
	}
	storage.SortLinks(links)
	return links, nil
}

func (fs Firestore) doc(ctx context.Context, ref *firestore.DocumentRef) (*document, error) {
	snap, err := ref.Get(ctx)
	if err != nil {
		return nil, err
	}
	result := &document{}
	if err := snap.DataTo(result); err != nil {
		return nil, fmt.Errorf("%w: %s", storage.ErrCorrupt, err)
	}
	return result, nil
}

// urlToPath encodes the canonical escaped path without changing its identity.
func urlToPath(u *url.URL) string {
	from := canonicalSource(u)
	id := "p-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(from.EscapedPath())))
	return strings.Join([]string{FirestoreCollection, from.Host, PathCollection, id}, "/")
}

func canonicalSource(u *url.URL) *url.URL {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// Hex case does not change URI identity; escaped reserved characters
	// remain distinct from their literal forms.
	b := []byte(p)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '%' {
			b[i+1] = upperHex(b[i+1])
			b[i+2] = upperHex(b[i+2])
			i += 2
		}
	}
	p = string(b)
	decoded, _ := url.PathUnescape(p) // EscapedPath always returns valid escaping.
	domain, err := shortlink.CanonicalDomain(u.Hostname())
	if err != nil {
		domain = strings.ToLower(u.Hostname())
	}
	return &url.URL{Host: domain, Path: decoded, RawPath: p}
}

func upperHex(b byte) byte {
	if b >= 'a' && b <= 'f' {
		return b - ('a' - 'A')
	}
	return b
}

func (fs Firestore) sourceRef(u *url.URL) (*firestore.DocumentRef, error) {
	if u == nil {
		return nil, storage.ErrInvalidSource
	}
	if _, err := shortlink.CanonicalDomain(u.Hostname()); err != nil {
		return nil, storage.ErrInvalidSource
	}
	p := urlToPath(u)
	id := p[strings.LastIndex(p, "/")+1:]
	if len(id) > 1500 {
		return nil, fmt.Errorf("%w: encoded path exceeds Firestore's 1500-byte document ID limit", storage.ErrInvalidSource)
	}
	return fs.Client.Doc(p), nil
}

func instrument(ctx context.Context) {
	trace.SpanFromContext(ctx).SetAttributes(semconv.DBSystemKey.String("firestore"), attribute.String("x40.storage.key_version", "base32-v1"))
}
