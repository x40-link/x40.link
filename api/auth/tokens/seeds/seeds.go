// Package seeds provides a way to "seed" a token source; especially one that relies on caching. This allows using
// the "seed" to bootstrap authentication in the case the cache is empty (or new), but doesn't call it every time.
package seeds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/oauth2"
)

// Err* are sentinel errors
var (
	ErrFailed = errors.New("seed failed")

	ErrFailedToExchange     = errors.New("failed to exchange token")
	ErrFailedToGetDeviceURL = errors.New("failed to get device url")
)

// Seed is a function that handles all of the initial authentication, so that we have a token (complete with refresh).
type Seed func(context.Context) (*oauth2.Token, error)

// DeviceAuth returns a seed that creates the initial OAuth2 flow via the Device flow. See:
// https://oauth.net/2/device-flow/
func DeviceAuth(audience string, c *oauth2.Config) Seed {
	return func(ctx context.Context) (*oauth2.Token, error) {
		// Initialize the token source with the full flow.
		response, err := c.DeviceAuth(ctx, oauth2.SetAuthURLParam("audience", audience))
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrFailedToGetDeviceURL, err)
		}

		fmt.Printf(
			"Please enter the code %s at %s\n",
			response.UserCode,
			response.VerificationURI,
		)

		tok, err := c.DeviceAccessToken(ctx, response, oauth2.AccessTypeOffline)

		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrFailedToExchange, err)
		}
		if tok.RefreshToken == "" {
			slog.Warn("login provider did not issue a refresh token; login will be required again after the access token expires")
		}

		return tok, nil
	}
}
