package memory

import (
	"context"
	"net/url"
	"sync"

	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/andrewhowdencom/x40.link/storage"
)

// HashTable stores the entire dataset within Go's implementation of a hash table (a map). It
// has O(1) complexity, as it is always looking up something well known within a finite space.
type HashTable struct {
	table    map[string]*url.URL
	records  map[string]storage.ManagedLink
	requests map[string]managedRequest
	mu       sync.RWMutex
}

// NewHashTable initializes a new hash table, with the appropriate default values. It also exposes the hash
// table outside this package, without needing to expose its internal properties (e.g. the table and mutexes)
// and so on.
func NewHashTable() *HashTable {
	return &HashTable{
		table:    make(map[string]*url.URL),
		records:  make(map[string]storage.ManagedLink),
		requests: make(map[string]managedRequest),
		mu:       sync.RWMutex{},
	}
}

// Get fetches a URL. It looks it up in the hashmap by converting it to a string representation (which should be
// unique), after which it will lookup the corresponding URL.
func (ht *HashTable) Get(_ context.Context, in *url.URL) (*url.URL, error) {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	if in != nil {
		path := in.EscapedPath()
		if path == "" {
			path = "/"
		}
		if name, err := shortlink.ResourceName(in.Hostname(), path); err == nil {
			if link, ok := ht.records[name]; ok {
				return url.Parse(link.DestinationURL)
			}
		}
	}

	if v, ok := ht.table[in.String()]; ok {
		return v, nil
	}

	return nil, storage.ErrNotFound
}

// Put writes a URL into memory. Designed to be used primarily via "loader" infrastructure, such as the
// YAML loader.
func (ht *HashTable) Put(_ context.Context, f *url.URL, t *url.URL) error {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	ht.table[f.String()] = t

	return nil
}

func (ht *HashTable) List(_ context.Context, domain string) ([]storage.Link, error) {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	links := make([]storage.Link, 0)
	for key, to := range ht.table {
		from, err := url.Parse(key)
		if err != nil {
			return nil, storage.ErrCorrupt
		}
		if domain == "" || from.Host == domain {
			links = append(links, storage.Link{From: from, To: to})
		}
	}
	storage.SortLinks(links)
	return links, nil
}
