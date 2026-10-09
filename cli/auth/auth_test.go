package auth

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/andrewhowdencom/x40.link/api"
	"github.com/andrewhowdencom/x40.link/api/auth/tokens/seeds"
	"github.com/andrewhowdencom/x40.link/api/auth/tokens/storage"
	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"
)

func TestOAuthConfigRequestsOfflineAccess(t *testing.T) {
	scopes := oauthConfig().Scopes
	assert.ElementsMatch(t, append(api.X40PermissionsList(), "offline_access"), scopes)
}

func TestLogin(t *testing.T) {
	t.Parallel()

	oldToken := []byte(`{"access_token":"old-access-token","token_type":"Bearer","refresh_token":"old-refresh-token"}`)
	newToken := &oauth2.Token{
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
	seedErr := errors.New("device authorization failed")
	writeErr := errors.New("cache write failed")

	for _, tc := range []struct {
		name string
		seed seeds.Seed
		str  storage.Storage

		wantToken      *oauth2.Token
		wantStored     []byte
		wantErr        error
		wantSeedCalled bool
	}{
		{
			name: "successful login replaces existing token",
			seed: func(_ context.Context) (*oauth2.Token, error) {
				return newToken, nil
			},
			str:            storage.NewTest(storage.WithBytes(oldToken)),
			wantToken:      newToken,
			wantSeedCalled: true,
		},
		{
			name: "failed login preserves existing token",
			seed: func(_ context.Context) (*oauth2.Token, error) {
				return nil, seedErr
			},
			str:            storage.NewTest(storage.WithBytes(oldToken)),
			wantStored:     oldToken,
			wantErr:        seedErr,
			wantSeedCalled: true,
		},
		{
			name: "cancelled login preserves existing token",
			seed: func(ctx context.Context) (*oauth2.Token, error) {
				return nil, ctx.Err()
			},
			str:            storage.NewTest(storage.WithBytes(oldToken)),
			wantStored:     oldToken,
			wantErr:        context.Canceled,
			wantSeedCalled: true,
		},
		{
			name: "write failure is returned",
			seed: func(_ context.Context) (*oauth2.Token, error) {
				return newToken, nil
			},
			str: storage.NewTest(
				storage.WithBytes(oldToken),
				storage.WithWriteError(func(_ *storage.Test, _ []byte) error {
					return writeErr
				}),
			),
			wantStored:     oldToken,
			wantErr:        writeErr,
			wantSeedCalled: true,
		},
	} {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if errors.Is(tc.wantErr, context.Canceled) {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			seedCalled := false
			seed := func(ctx context.Context) (*oauth2.Token, error) {
				seedCalled = true
				return tc.seed(ctx)
			}

			err := login(ctx, seed, tc.str)

			assert.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.wantSeedCalled, seedCalled)

			stored, readErr := tc.str.Read()
			assert.NoError(t, readErr)

			if tc.wantToken != nil {
				got := &oauth2.Token{}
				assert.NoError(t, json.Unmarshal(stored, got))
				assert.Equal(t, tc.wantToken.AccessToken, got.AccessToken)
				assert.Equal(t, tc.wantToken.RefreshToken, got.RefreshToken)
				assert.Equal(t, tc.wantToken.TokenType, got.TokenType)
			} else {
				assert.Equal(t, tc.wantStored, stored)
			}
		})
	}
}
