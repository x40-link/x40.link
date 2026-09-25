// Package auth provides facilities to generate the auth required for connecting to the API
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/andrewhowdencom/x40.link/api"
	"github.com/andrewhowdencom/x40.link/api/auth/jwts"
	"github.com/andrewhowdencom/x40.link/api/auth/tokens"
	"github.com/andrewhowdencom/x40.link/api/auth/tokens/seeds"
	"github.com/andrewhowdencom/x40.link/api/auth/tokens/storage"
	"github.com/andrewhowdencom/x40.link/cfg"
	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

// TokenSource returns a TokenSource appropriate for the CLI Application, or an error if this failed.
func TokenSource() (oauth2.TokenSource, error) {
	ctx := context.Background()
	oauthCfg := oauthConfig()

	str, err := tokenStorage()
	if err != nil {
		return nil, err
	}

	ts, err := tokens.NewCachingSource(
		ctx,
		oauthCfg.TokenSource,
		seeds.DeviceAuth(jwts.AudienceX40API, oauthCfg),
		str,
	)
	if err != nil {
		return nil, err
	}

	return ts, nil
}

// Login runs a fresh device authorization flow and replaces the cached token
// only after authentication succeeds.
func Login(ctx context.Context) error {
	oauthCfg := oauthConfig()

	str, err := tokenStorage()
	if err != nil {
		return fmt.Errorf("prepare token storage: %w", err)
	}

	return login(ctx, seeds.DeviceAuth(jwts.AudienceX40API, oauthCfg), str)
}

func login(ctx context.Context, seed seeds.Seed, str storage.Storage) error {
	tok, err := seed(ctx)
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	encoded, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}

	if err := str.Write(encoded); err != nil {
		return fmt.Errorf("store token: %w", err)
	}

	return nil
}

func oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID: viper.GetString(cfg.OAuth2ClientID.Path),
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: viper.GetString(cfg.OAuth2DeviceAuthorizationEndpoint.Path),
			TokenURL:      viper.GetString(cfg.OAuth2TokenURL.Path),
		},
		Scopes: append(api.X40PermissionsList(), "offline_access"),
	}
}

func tokenStorage() (storage.Storage, error) {
	tokPath, err := xdg.DataFile(filepath.Join("x40", "cli-token"))
	if err != nil {
		return nil, err
	}

	return &storage.File{Path: tokPath}, nil
}
