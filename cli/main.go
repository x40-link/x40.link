// package main is the main package of the CLI client
package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/andrewhowdencom/sysexits"
	"github.com/andrewhowdencom/x40.link/api"
	"github.com/andrewhowdencom/x40.link/cfg"
	"github.com/andrewhowdencom/x40.link/cli/auth"
	"github.com/andrewhowdencom/x40.link/cmd"
	"github.com/andrewhowdencom/x40.link/shortlink"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	v1alpha "github.com/x40-link/api/gen/x40/link/v1alpha"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Flag sets.
//
// The CLI exposes two kinds of commands: those that need a backend gRPC client
// ("api") and those that additionally need OAuth configuration ("auth"). The
// root command (which creates a short link) needs both; "resolve" needs only the
// API endpoint, while "login" needs only OAuth configuration.
var (
	apiFlagSet = func() *pflag.FlagSet {
		fs := &pflag.FlagSet{}

		for _, f := range []interface {
			AddFlagTo(*pflag.FlagSet)
		}{
			cfg.APIEndpoint,
		} {
			f.AddFlagTo(fs)
		}

		return fs
	}()

	authFlagSet = func() *pflag.FlagSet {
		fs := &pflag.FlagSet{}

		for _, f := range []interface {
			AddFlagTo(*pflag.FlagSet)
		}{
			cfg.OAuth2ClientID,
			cfg.OAuth2AuthorizationURL,
			cfg.OAuth2DeviceAuthorizationEndpoint,
			cfg.OAuth2TokenURL,
		} {
			f.AddFlagTo(fs)
		}

		return fs
	}()

	// urlFlagSet composes both sets for the root command. Subcommands attach
	// only the configuration they need.
	urlFlagSet = func() *pflag.FlagSet {
		fs := &pflag.FlagSet{}

		fs.AddFlagSet(apiFlagSet)
		fs.AddFlagSet(authFlagSet)

		return fs
	}()
)

// resolveTimeout is the timeout used for the gRPC Get call. It is intentionally
// the same as the root command's timeout (10s); the operation is read-only and
// does not need a longer window.
const resolveTimeout = 10 * time.Second

// Root represents the url command
var Root = &cobra.Command{
	Use:   "@",
	Short: "The client tool for generating URLs",
	Long: `Create a short link:

    @ https://my.destination.url/path
    @ source.domain https://my.destination.url/path
    @ https://source.domain/path https://my.destination.url/path

Omit the source to use x40.link; omit its path to generate one.
Custom source domains need DNS routing to this service for redirects to work.`,
	Args: cobra.MinimumNArgs(1),
	RunE: DoURL,
}

// resolveCmd looks up a link through the authenticated management API.
var resolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Look up the destination of a short link",
	Long: `Look up the destination of a short link.

Given a short URL, print the URL it redirects to. The command does not
require authentication. Anonymous users can still follow the HTTP redirect.

Example:

    @ resolve https://x40.link/abc
`,
	Args: cobra.ExactArgs(1),
	RunE: DoResolve,
}

// loginCmd is the "login" subcommand. It always runs a fresh OAuth device
// authorization flow and replaces cached credentials only after success.
var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in with a different account",
	Long: `Log in with a different account.

Start a fresh OAuth device authorization flow. Existing cached credentials
remain available if authentication fails or is cancelled.

Example:

    @ login
`,
	Args: cobra.NoArgs,
	RunE: DoLogin,
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List links visible to your account",
	Long: `List short links visible to your account.

Filter by source domain with --domain. Results are scoped to your account.

Examples:

    @ list
    @ list --domain x40.link
`,
	Args: cobra.NoArgs,
	RunE: DoList,
}

// DoURL is the root command for the client, and generates URLs
func DoURL(_ *cobra.Command, args []string) error {
	req, err := buildNewRequest(args)
	if err != nil {
		return err
	}

	ts, err := auth.TokenSource()
	if err != nil {
		return fmt.Errorf("%w: %s", sysexits.Software, err)
	}

	client, err := api.NewGRPCClient(
		viper.GetString(cfg.APIEndpoint.Path),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(ts)),
	)
	if err != nil {
		return fmt.Errorf("%w: %s", sysexits.NoHost, err)
	}

	ctx, cxl := context.WithTimeout(context.Background(), time.Second*10)
	defer cxl()

	resp, err := client.CreateShortLink(ctx, req)

	if err != nil {
		return fmt.Errorf("%w: %s", sysexits.Protocol, err)
	}

	fmt.Println(resp.ShortUrl)

	return nil
}

func buildNewRequest(args []string) (*v1alpha.CreateShortLinkRequest, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("%w: expected destination and optional source", sysexits.Usage)
	}
	normalized := make([]string, len(args))
	for i, arg := range args {
		if !strings.Contains(arg, "://") {
			arg = "https://" + arg
		}
		normalized[i] = arg
	}
	req := &v1alpha.CreateShortLinkRequest{
		Parent:    "domains/x40.link",
		ShortLink: &v1alpha.ShortLink{DestinationUrl: normalized[len(normalized)-1]},
	}
	if len(normalized) == 2 {
		u, err := url.Parse(normalized[0])
		if err != nil || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("%w: invalid source URL", sysexits.DataErr)
		}
		domain, err := shortlink.CanonicalDomain(u.Hostname())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", sysexits.DataErr, err)
		}
		req.Parent = "domains/" + domain
		if u.EscapedPath() != "" {
			path, err := shortlink.CanonicalPath(u.EscapedPath())
			if err != nil {
				return nil, fmt.Errorf("%w: %v", sysexits.DataErr, err)
			}
			req.ShortLink.Path = &path
		}
	}
	return req, nil
}

// DoLogin is the cobra command handler for the "login" subcommand.
func DoLogin(cmd *cobra.Command, _ []string) error {
	if err := doLogin(cmd.Context(), auth.Login); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Login successful."); err != nil {
		return fmt.Errorf("%w: write login confirmation: %w", sysexits.Software, err)
	}

	return nil
}

func doLogin(ctx context.Context, login func(context.Context) error) error {
	if err := login(ctx); err != nil {
		return fmt.Errorf("%w: %w", sysexits.Software, err)
	}

	return nil
}

// DoResolve is the cobra command handler for the "resolve" subcommand. It builds
// a gRPC client with per-RPC credentials and
// delegates the actual call to doResolveWithClient for testability.
func DoResolve(_ *cobra.Command, args []string) error {
	ts, err := auth.TokenSource()
	if err != nil {
		return fmt.Errorf("%w: %v", sysexits.Software, err)
	}
	client, err := api.NewGRPCClient(viper.GetString(cfg.APIEndpoint.Path), grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(ts)))
	if err != nil {
		return fmt.Errorf("%w: %s", sysexits.NoHost, err)
	}

	ctx, cxl := context.WithTimeout(context.Background(), resolveTimeout)
	defer cxl()

	destination, err := doResolveWithClient(ctx, client, args[0])
	if err != nil {
		return err
	}

	fmt.Println(destination)
	return nil
}

// DoList fetches links through the authenticated API and prints one mapping per line.
func DoList(cmd *cobra.Command, _ []string) error {
	domain, err := cmd.Flags().GetString("domain")
	if err != nil {
		return fmt.Errorf("%w: %s", sysexits.Software, err)
	}
	ts, err := auth.TokenSource()
	if err != nil {
		return fmt.Errorf("%w: %s", sysexits.Software, err)
	}
	client, err := api.NewGRPCClient(
		viper.GetString(cfg.APIEndpoint.Path),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(ts)),
	)
	if err != nil {
		return fmt.Errorf("%w: %s", sysexits.NoHost, err)
	}
	return runList(cmd.Context(), client, domain, cmd.OutOrStdout())
}

func runList(ctx context.Context, client api.Client, domain string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	links, err := doListWithClient(ctx, client, domain)
	if err != nil {
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	for _, link := range links {
		if _, err := fmt.Fprintf(writer, "%s\t%s\n", link.ShortUrl, link.DestinationUrl); err != nil {
			return fmt.Errorf("%w: %s", sysexits.Software, err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("%w: %s", sysexits.Software, err)
	}
	return nil
}

func doListWithClient(ctx context.Context, client api.Client, domain string) ([]*v1alpha.ShortLink, error) {
	parent := "domains/-"
	if domain != "" {
		canonical, err := shortlink.CanonicalDomain(domain)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", sysexits.DataErr, err)
		}
		parent = "domains/" + canonical
	}
	links := []*v1alpha.ShortLink{}
	token := ""
	seen := map[string]bool{}
	for {
		response, err := client.ListShortLinks(ctx, &v1alpha.ListShortLinksRequest{Parent: parent, PageToken: token})
		if err != nil {
			return nil, classifyResolveError(err)
		}
		links = append(links, response.ShortLinks...)
		if response.NextPageToken == "" {
			return links, nil
		}
		if seen[response.NextPageToken] {
			return nil, fmt.Errorf("%w: repeated list page token", sysexits.Protocol)
		}
		seen[response.NextPageToken] = true
		token = response.NextPageToken
	}
}

// doResolveWithClient is the testable core of the resolve flow. It takes a
// ready-to-use gRPC client, an input URL string, and returns the destination
// URL (with any leading "//" stripped, matching the DoURL convention) or a
// sysexits-wrapped error appropriate to the failure mode.
//
// The "https://" scheme is prepended to inputs that have no scheme, mirroring
// the existing DoURL behavior.
func doResolveWithClient(ctx context.Context, client api.Client, input string) (string, error) {
	if !strings.Contains(input, "://") {
		input = "https://" + input
	}

	u, err := url.Parse(input)
	if err != nil || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: invalid short URL", sysexits.DataErr)
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	name, err := shortlink.ResourceName(u.Hostname(), path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", sysexits.DataErr, err)
	}
	resp, err := client.GetShortLink(ctx, &v1alpha.GetShortLinkRequest{Name: name})
	if err != nil {
		return "", classifyResolveError(err)
	}

	return resp.DestinationUrl, nil
}

// classifyResolveError maps a gRPC error from the Get call to a sysexits code.
// NotFound and InvalidArgument both indicate "the input data is wrong", which
// maps to DataErr. A bare (non-gRPC) error suggests a transport failure and
// maps to NoHost. Other gRPC errors are treated as protocol failures.
func classifyResolveError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		// Not a gRPC status error — assume transport failure.
		return fmt.Errorf("%w: %s", sysexits.NoHost, err)
	}

	switch st.Code() {
	case codes.NotFound, codes.InvalidArgument:
		return fmt.Errorf("%w: %s", sysexits.DataErr, st.Message())
	default:
		return fmt.Errorf("%w: %s", sysexits.Protocol, st.Message())
	}
}

func init() {
	Root.Flags().AddFlagSet(urlFlagSet)
	Root.AddCommand(loginCmd, resolveCmd, listCmd)
	loginCmd.Flags().AddFlagSet(authFlagSet)
	resolveCmd.Flags().AddFlagSet(urlFlagSet)
	listCmd.Flags().AddFlagSet(urlFlagSet)
	listCmd.Flags().String("domain", "", "restrict results to this source domain")
}

func main() {
	// Cobra will print the exit.String() as part of its Execute method. Here, we only need to check
	// what the exit code should be.
	exit := cmd.Execute(Root)
	os.Exit(exit.Code)
}
