package firestore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const requestCollection = "shortLinkRequests"

type requestDocument struct {
	Owner       string    `firestore:"owner"`
	Fingerprint string    `firestore:"fingerprint"`
	Result      string    `firestore:"result"`
	Expires     time.Time `firestore:"expires"`
}

var _ storage.ManagedStore = Firestore{}

func (fs Firestore) managedRef(name string) (*firestore.DocumentRef, error) {
	domain, path, err := shortlink.ParseResourceName(name)
	if err != nil {
		return nil, storage.ErrInvalidSource
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return nil, storage.ErrInvalidSource
	}
	return fs.sourceRef(&url.URL{Host: domain, Path: decoded, RawPath: path})
}

func (fs Firestore) requestRef(owner, requestID string) *firestore.DocumentRef {
	hash := sha256.Sum256([]byte(owner + "\x00" + requestID))
	return fs.Client.Collection(requestCollection).Doc(hex.EncodeToString(hash[:]))
}

func decodeManaged(snap *firestore.DocumentSnapshot) (storage.ManagedLink, error) {
	var doc struct {
		From    string `firestore:"from"`
		To      string `firestore:"to"`
		Owner   string `firestore:"owner"`
		Managed string `firestore:"managed_json"`
	}
	if err := snap.DataTo(&doc); err != nil {
		return storage.ManagedLink{}, storage.ErrCorrupt
	}
	if doc.Managed != "" {
		var link storage.ManagedLink
		if err := json.Unmarshal([]byte(doc.Managed), &link); err != nil {
			return storage.ManagedLink{}, storage.ErrCorrupt
		}
		if link.Owner != doc.Owner || link.DestinationURL != doc.To {
			return storage.ManagedLink{}, storage.ErrCorrupt
		}
		return link, nil
	}
	// Existing owned documents remain manageable after the contract cutover.
	// Their immutable UID is derived from their key; their ETag changes with
	// the Firestore document update time until the first v1alpha mutation.
	if doc.From == "" || doc.To == "" {
		return storage.ManagedLink{}, storage.ErrCorrupt
	}
	from, err := url.Parse(doc.From)
	if err != nil {
		return storage.ManagedLink{}, storage.ErrCorrupt
	}
	path := from.EscapedPath()
	if path == "" {
		path = "/"
	}
	name, err := shortlink.ResourceName(from.Hostname(), path)
	if err != nil {
		return storage.ManagedLink{}, storage.ErrCorrupt
	}
	uidHash := sha256.Sum256([]byte(snap.Ref.Path))
	etagHash := sha256.Sum256([]byte(snap.Ref.Path + "\x00" + snap.UpdateTime.String()))
	return storage.ManagedLink{
		Name: name, Path: path, DestinationURL: doc.To, Owner: doc.Owner,
		UID:        "legacy-" + base64.RawURLEncoding.EncodeToString(uidHash[:16]),
		CreateTime: snap.CreateTime.UTC(), UpdateTime: snap.UpdateTime.UTC(),
		ETag: `"` + hex.EncodeToString(etagHash[:]) + `"`,
	}, nil
}

func managedData(link storage.ManagedLink) (map[string]interface{}, error) {
	domain, _, err := shortlink.ParseResourceName(link.Name)
	if err != nil || link.Owner == "" {
		return nil, storage.ErrInvalidSource
	}
	if link.Path == "" || link.Name == "" {
		return nil, storage.ErrInvalidSource
	}
	encoded, err := json.Marshal(link)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", storage.ErrInvalidSource, err)
	}
	return map[string]interface{}{
		"from":         "//" + domain + link.Path,
		"to":           link.DestinationURL,
		"owner":        link.Owner,
		"managed_json": string(encoded),
	}, nil
}

func (fs Firestore) CreateManaged(ctx context.Context, link storage.ManagedLink, requestID, fingerprint string, now time.Time) (storage.ManagedLink, error) {
	ref, err := fs.managedRef(link.Name)
	if err != nil {
		return storage.ManagedLink{}, err
	}
	data, err := managedData(link)
	if err != nil {
		return storage.ManagedLink{}, err
	}
	if requestID != "" && fingerprint == "" {
		return storage.ManagedLink{}, storage.ErrInvalidSource
	}
	var result storage.ManagedLink
	err = fs.Client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var previous requestDocument
		if requestID != "" {
			snap, err := tx.Get(fs.requestRef(link.Owner, requestID))
			if err != nil && status.Code(err) != codes.NotFound {
				return err
			}
			if err == nil {
				if err := snap.DataTo(&previous); err != nil {
					return storage.ErrCorrupt
				}
				if now.Before(previous.Expires) {
					if previous.Owner != link.Owner || previous.Fingerprint != fingerprint {
						return storage.ErrRequestIDConflict
					}
					if err := json.Unmarshal([]byte(previous.Result), &result); err != nil {
						return storage.ErrCorrupt
					}
					return nil
				}
			}
		}
		snap, err := tx.Get(ref)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if err == nil {
			var existing document
			if err := snap.DataTo(&existing); err != nil {
				return storage.ErrCorrupt
			}
			if existing.Owner != link.Owner {
				return storage.ErrUnauthorized
			}
			return storage.ErrAlreadyExists
		}
		if err := tx.Create(ref, data); err != nil {
			return err
		}
		if requestID != "" {
			encoded, _ := json.Marshal(link)
			req := requestDocument{Owner: link.Owner, Fingerprint: fingerprint, Result: string(encoded), Expires: now.Add(24 * time.Hour)}
			if previous.Owner == "" {
				return tx.Create(fs.requestRef(link.Owner, requestID), req)
			}
			return tx.Set(fs.requestRef(link.Owner, requestID), req)
		}
		result = link.Clone()
		return nil
	})
	if err != nil {
		if status.Code(err) == codes.Aborted {
			if requestID != "" {
				if retained, requestErr := fs.GetRequest(ctx, link.Owner, requestID, fingerprint, now); requestErr == nil {
					return retained, nil
				}
			}
			if snap, readErr := ref.Get(ctx); readErr == nil {
				var existing document
				if decodeErr := snap.DataTo(&existing); decodeErr != nil {
					return storage.ManagedLink{}, storage.ErrCorrupt
				}
				if existing.Owner != link.Owner {
					return storage.ManagedLink{}, storage.ErrUnauthorized
				}
				return storage.ManagedLink{}, storage.ErrAlreadyExists
			}
		}
		return storage.ManagedLink{}, managedError(err)
	}
	if result.Name == "" {
		result = link.Clone()
	}
	return result, nil
}

func (fs Firestore) GetRequest(ctx context.Context, owner, requestID, fingerprint string, now time.Time) (storage.ManagedLink, error) {
	if owner == "" {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	snap, err := fs.requestRef(owner, requestID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return storage.ManagedLink{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.ManagedLink{}, managedError(err)
	}
	var doc requestDocument
	if err := snap.DataTo(&doc); err != nil {
		return storage.ManagedLink{}, storage.ErrCorrupt
	}
	if !now.Before(doc.Expires) {
		return storage.ManagedLink{}, storage.ErrNotFound
	}
	if doc.Owner != owner || doc.Fingerprint != fingerprint {
		return storage.ManagedLink{}, storage.ErrRequestIDConflict
	}
	var result storage.ManagedLink
	if err := json.Unmarshal([]byte(doc.Result), &result); err != nil {
		return storage.ManagedLink{}, storage.ErrCorrupt
	}
	return result, nil
}

func (fs Firestore) GetManaged(ctx context.Context, name, owner string) (storage.ManagedLink, error) {
	if owner == "" {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	ref, err := fs.managedRef(name)
	if err != nil {
		return storage.ManagedLink{}, err
	}
	snap, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return storage.ManagedLink{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.ManagedLink{}, managedError(err)
	}
	var doc document
	if err := snap.DataTo(&doc); err != nil {
		return storage.ManagedLink{}, storage.ErrCorrupt
	}
	if doc.Owner != owner {
		return storage.ManagedLink{}, storage.ErrUnauthorized
	}
	link, err := decodeManaged(snap)
	if err != nil || link.Name != name {
		return storage.ManagedLink{}, storage.ErrCorrupt
	}
	return link, nil
}

func (fs Firestore) ListManaged(ctx context.Context, owner, domain string) ([]storage.ManagedLink, error) {
	if owner == "" {
		return nil, storage.ErrUnauthorized
	}
	var snaps []*firestore.DocumentSnapshot
	var err error
	if domain == "" {
		snaps, err = fs.Client.CollectionGroup(PathCollection).Where("owner", "==", owner).Documents(ctx).GetAll()
	} else {
		snaps, err = fs.Client.Collection(FirestoreCollection).Doc(domain).Collection(PathCollection).Where("owner", "==", owner).Documents(ctx).GetAll()
	}
	if err != nil {
		return nil, managedError(err)
	}
	links := make([]storage.ManagedLink, 0, len(snaps))
	for _, snap := range snaps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		parent := snap.Ref.Parent.Parent
		if parent == nil || parent.Parent == nil || parent.Parent.ID != FirestoreCollection || parent.Parent.Parent != nil {
			continue
		}
		link, err := decodeManaged(snap)
		if err != nil {
			return nil, err
		}
		if link.Owner != owner || domain != "" && !strings.HasPrefix(link.Name, "domains/"+domain+"/shortLinks/") {
			continue
		}
		links = append(links, link)
	}
	sort.Slice(links, func(i, j int) bool { return links[i].Name < links[j].Name })
	return links, nil
}

func (fs Firestore) UpdateManaged(ctx context.Context, link storage.ManagedLink, previousETag string) (storage.ManagedLink, error) {
	ref, err := fs.managedRef(link.Name)
	if err != nil {
		return storage.ManagedLink{}, err
	}
	data, err := managedData(link)
	if err != nil {
		return storage.ManagedLink{}, err
	}
	err = fs.Client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return storage.ErrNotFound
		}
		if err != nil {
			return err
		}
		current, err := decodeManaged(snap)
		if err != nil {
			return err
		}
		if current.Owner != link.Owner {
			return storage.ErrUnauthorized
		}
		if previousETag == "" || current.ETag != previousETag || current.UID != link.UID {
			return storage.ErrAborted
		}
		if current.Path != link.Path || !current.CreateTime.Equal(link.CreateTime) {
			return storage.ErrInvalidSource
		}
		return tx.Set(ref, data)
	})
	if err != nil {
		return storage.ManagedLink{}, managedError(err)
	}
	return link.Clone(), nil
}

func (fs Firestore) DeleteManaged(ctx context.Context, name, owner, expectedETag string) error {
	if owner == "" {
		return storage.ErrUnauthorized
	}
	ref, err := fs.managedRef(name)
	if err != nil {
		return err
	}
	err = fs.Client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return storage.ErrNotFound
		}
		if err != nil {
			return err
		}
		current, err := decodeManaged(snap)
		if err != nil {
			return err
		}
		if current.Owner != owner {
			return storage.ErrUnauthorized
		}
		if expectedETag != "" && current.ETag != expectedETag {
			return storage.ErrAborted
		}
		return tx.Delete(ref)
	})
	return managedError(err)
}

func managedError(err error) error {
	if err == nil || errors.Is(err, storage.ErrCorrupt) || errors.Is(err, storage.ErrNotFound) ||
		errors.Is(err, storage.ErrUnauthorized) || errors.Is(err, storage.ErrAlreadyExists) ||
		errors.Is(err, storage.ErrAborted) || errors.Is(err, storage.ErrRequestIDConflict) ||
		errors.Is(err, storage.ErrInvalidSource) {
		return err
	}
	return fmt.Errorf("%w: %v", storage.ErrFailed, err)
}
