// Package tokens provides an implementation to fetch and manage OAuth2 tokens.
package tokens

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/andrewhowdencom/x40.link/api/auth/tokens/seeds"
	"github.com/andrewhowdencom/x40.link/api/auth/tokens/storage"
	"golang.org/x/oauth2"
)

// Err* are sentinel errors for this package.
var (
	ErrCacheSetupFailed = errors.New("cache setup failed")
	ErrCacheFailure     = errors.New("cache failure")
)

// CachingSource is an implementation of TokenSource that will attempt to read the token from a cache.
// If it finds a valid token in the case, it'll use it. If not, it'll use the supplied "seed function"
// to fetch a new token.
type CachingSource struct {
	ts oauth2.TokenSource

	// Cur is the previously seen access token.
	cur *oauth2.Token

	// Storage is the me
	str storage.Storage
}

// NewReuseTokenSource is the anticipated default token source for the caching source to wrap.
func NewReuseTokenSource(cfg *oauth2.Config) func(context.Context, *oauth2.Token) oauth2.TokenSource {
	return func(ctx context.Context, t *oauth2.Token) oauth2.TokenSource {
		return oauth2.ReuseTokenSource(t, cfg.TokenSource(ctx, t))
	}
}

// NewCachingSource allows creating a oauth2.TokenSource that caches the results in a storage layer (provided when
// the thing is constructed). Allows seeding the token source with a new token if there is nothing in the cache,
// or if what is in the cache is invalid (e.g. the user has never signed in before).
func NewCachingSource(
	ctx context.Context,
	tsf func(context.Context, *oauth2.Token) oauth2.TokenSource,
	sf seeds.Seed,
	str storage.Storage,
) (*CachingSource, error) {
	cs := &CachingSource{
		ts:  nil,
		cur: nil,
		str: str,
	}

	var needsSeed bool

	bytes, err := cs.str.Read()
	tok := &oauth2.Token{}

	if errors.Is(err, storage.ErrNotFound) {
		needsSeed = true
	} else if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCacheSetupFailed, err)
	}

	if !needsSeed {
		err := json.Unmarshal(bytes, tok)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrCacheFailure, err)
		}

		if !tok.Valid() && tok.RefreshToken == "" {
			slog.Warn("cached login expired without a refresh token; starting device login")
			needsSeed = true
		}
	}

	// If the token is not found, or the retrieved token is not valid, request a new token.
	if needsSeed {
		// If there is no token in the cache, we need to run the seed.
		tok, err = sf(ctx)

		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrCacheSetupFailed, err)
		}

		// It should not be possible to receive a token that cannot be serialized to JSON, but just in case,
		// handle this
		bytes, err := json.Marshal(tok)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrCacheFailure, err)
		}

		// Write the seeded token into the cache
		if err := cs.str.Write(bytes); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrCacheSetupFailed, err)
		}
	}

	cs.cur = tok
	cs.ts = tsf(ctx, cs.cur)

	return cs, nil
}

// Token implements the oauth2.TokenSource interface, allowing the user to query a token — wherever it comes
// from.
func (cs *CachingSource) Token() (*oauth2.Token, error) {
	newTok, err := cs.ts.Token()

	// Here, we're not wrapping the error as we're a cache for the underlying storage. That error can be handled
	// directly, if needed.
	if err != nil {
		return nil, err
	}

	if !isDifferent(cs.cur, newTok) {
		return newTok, nil
	}

	bytes, err := json.Marshal(newTok)
	if err != nil {
		return nil, err
	}

	if err := cs.str.Write(bytes); err != nil {
		return nil, err
	}

	cs.cur = newTok
	return newTok, nil
}

func isDifferent(a *oauth2.Token, b *oauth2.Token) bool {
	// If they're both nil, then there is no difference
	if a == nil && b == nil {
		return false
	}

	// If there is one that is nil, they are for sure different.
	if (a != nil && b == nil) || (a == nil && b != nil) {
		return true
	}

	// If there's a different expiry, its different.
	if a.Expiry.Sub(b.Expiry) != 0 {
		return true
	}

	// Token Types
	if a.AccessToken != b.AccessToken {
		return true
	}

	if a.RefreshToken != b.RefreshToken {
		return true
	}

	return false
}
