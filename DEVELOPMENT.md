# Development

This document is for engineers working on the x40.link codebase. It covers
project layout, design notes, and behavior that isn't obvious from reading
the code.

## CLI Releases

Publish a GitHub release against a tag containing
`.github/workflows/release-cli.yml`. Publishing either a stable release or a
prerelease starts the workflow; saving a draft or pushing a tag alone does
not. The workflow checks out that tag, tests the CLI, cross-compiles all
seven supported targets, verifies the archives, and attaches them alongside
`SHA256SUMS` to the same release. No additional secret is needed: uploads
use the workflow's `GITHUB_TOKEN` with `contents: write` permission.

Each `x40-cli_<os>_<arch>.tar.gz` archive contains only `@` (or `@.exe` for
Windows). Linux ARM builds target ARMv7. All builds disable CGO and embed
the release tag using the existing version linker flag. Archive names stay
constant across releases. If an upload fails, rerun the workflow from
GitHub Actions; existing assets with matching names will be replaced.

Packaging and verification are defined directly in `Taskfile.yml`.
To build and validate the same artifacts locally, install Go, Task,
GNU tar and `sha256sum`, then run:

```bash
task tools/go/install
RELEASE_VERSION=v1.2.3 task release/cli/test
```

Ensure Go's binary directory is on `PATH` for the protobuf tools.
`task release/cli` builds without running the validation suite. Both tasks
write to `dist/cli-release/`; omitting `RELEASE_VERSION` uses the short Git
commit hash. Validation checks the checksums, archive contents and target
metadata, runs the host binary's help commands, and runs the CLI unit tests.
Run it on one of the supported host platforms with the tools listed above.

## Server Deployments

The main server workflow authenticates to Google Cloud through Workload
Identity Federation. Its service account trusts the repository attribute
`x40-link/x40.link`; the binding is managed in `deploy/prod/tf/actions.tf`.
After a repository transfer or rename, update and apply this binding before
rerunning deployments. A stale repository attribute causes the authentication
step to fail with `iam.serviceAccounts.getAccessToken` permission denied.

`task container/all` publishes to Google Artifact Registry and
`ghcr.io/x40-link/x40.link`. The GitHub registry namespace must match the
repository owner so the workflow's `GITHUB_TOKEN` can publish the image.

## CLI Subcommands

The CLI binary lives in `cli/`. It exposes four operations:

* **`@ <url>`** (root command) — create a short link. Requires OAuth
  credentials via the device authorization flow. See `cli/main.go::DoURL`.
* **`@ login`** — run a fresh device authorization flow and replace the
  cached token. It does not call the x40 API. See `cli/main.go::DoLogin` and
  `cli/auth/auth.go::Login`.
* **`@ resolve <url>`** — look up the destination of a short link. Does
  *not* require OAuth credentials. See `cli/main.go::DoResolve` and
  `cli/main.go::doResolveWithClient`.
* **`@ list [--domain <host>]`** — list short URLs and destinations.
  Requires OAuth credentials. Firestore filters by the authenticated
  subject; backends without ownership data return every matching link.
  The domain filter applies to the short URL's host.

The flag sets are split into `apiFlagSet` (just `cfg.APIEndpoint`) and
`authFlagSet` (the OAuth-related flags). The root command uses both
(composed into `urlFlagSet`), `login` uses only `authFlagSet`, `resolve`
uses only `apiFlagSet`, and `list` uses both. Adding a new subcommand with a
different set of configuration means attaching the right flag set to the new
Cobra command.

Explicit login is transactional from the CLI's perspective. `auth.Login`
completes the device flow and serializes the returned token before calling
the existing atomic token-storage write. Authentication, cancellation, or
write failures therefore leave the previously cached credentials unchanged.
The CLI requests `offline_access` alongside API permissions during device
authorization. Auth0 requires that scope to issue a refresh token, even though
the API already allows offline access. The cached token source saves renewed
tokens, including rotated refresh tokens, for later CLI invocations. Users
with tokens issued before this scope was requested need to run `@ login` once
after upgrading.

## Public vs. Authenticated gRPC Methods

The gRPC server in `api/dev/` enforces OAuth scope on a per-method basis,
via the JWT server interceptor in `api/auth/jwts/`. A method's scope is
declared in the proto file with the `oauth2_scope` extension. The
interceptor reads those scopes via `api/api.go::X40Permissions()` and
enforces them at call time.

A method whose declared scope is *absent* (or, equivalently, declared as
the empty string) is treated as **publicly callable**:

* No `Authorization` header is required.
* If one is supplied, it is still stripped from the outgoing context so
  the handler does not see credentials it doesn't need.
* No `storage.CtxKeyAgent` is attached to the context. Handlers of
  public methods must not assume an authenticated agent.

The `Get` method on `x40.dev.url.ManageURLs` is currently the only public
method. The destination of a short link is functionally public information
— the HTTP redirect at `server/storage.go::Redirect` already discloses
it to anonymous users — so the gRPC `Get` RPC aligns with that reality by
being public. This is what allows the `resolve` subcommand to work
without OAuth.

`List` requires the dedicated `ManageURLs.List` scope even when the
selected storage backend has no ownership model. Firestore takes the caller's
identity from the validated JWT and returns only their records. The API never
accepts an owner ID as a filter.

Firestore stores all links, including `/`, at
`links/<domain>/shortLinks/p-<encoded_path>`. The path component is lowercase
RFC 4648 Base32 without padding, encoding the escaped source path. Hosts are
lowercase, percent-escape hex digits are uppercase, and empty storage paths
resolve to `/`. Path case, plus signs, repeated slashes, and escaped reserved
characters remain distinct. Scheme and query do not select a different link.
The `p-` prefix makes the encoded component a resource ID; Firestore's
1,500-byte document ID limit allows at most 936 bytes of escaped path.

Firestore `Put` now claims an address atomically and never overwrites it.
An existing address returns `ErrAlreadyExists` to its owner and
`ErrUnauthorized` to another caller. `ManageURLs.New` maps these to
`ALREADY_EXISTS` and `PERMISSION_DENIED`. `RedirectOn.path` is an escaped
absolute path; an omitted path still requests a generated suffix.

The all-domain owner lookup needs the `shortLinks` collection-group index
defined in `deploy/prod/tf/firestore.tf`. Apply that index and migrate old
records before sending traffic to this version; there is no legacy read
fallback. See [Migrate Firestore path keys](docs/content/how-to/migrate-firestore-path-keys.md).
The legacy `id` index is retained for rollback. Listing currently returns
all matches in one response, so pagination will be needed as accounts grow.

Firestore operations annotate the existing request span with
`x40.storage.key_version=base32-v1` and `db.system=firestore` so the new
storage path can be identified in production traces.

The production Cloud Run request timeout is 60 seconds in
`deploy/prod/cr/service.yaml`. This allows cold starts and Firestore queries
to finish before the load balancer closes the connection; the CLI's list
request has its own 30-second deadline. The CLI formats list output in
aligned columns, so it is intended for display rather than tab-delimited
parsing.

When adding a new RPC, ask: is the response of this RPC already disclosed
to anonymous users by another path (e.g., the HTTP redirect handler, a
public website, etc.)? If so, declaring it as public — by omitting the
`oauth2_scope` extension on the method — is the right call. If the
response carries data that is genuinely not public, declare a scope.

There is a TODO in `api/dev/url.proto` acknowledging that "authentication
should be an emergent property of these definitions" and that the auth
model should be revisited when looking at ReBAC (relationship-based
access control). For now, the per-method scope annotation is the
authoritative way to mark a method as auth-required or public.

## Proto Regeneration

Generated code under `api/gen/` is gitignored (see `.gitignore`, under
"Generated API Files"). To regenerate it locally, run `task
protobuf/generate`, which runs `buf generate` from the `api/`
directory. The `buf.lock` pins the tool versions; do not commit a
diff in the lockfile unless you have intentionally upgraded `buf`.

## Observability

The server emits OpenTelemetry traces and metrics. The pipeline is
initialised in `otel.Init`, called from `cmd/serve.go::RunServe` before
the server starts listening. The OTel SDK is configured via the existing
`cfg/cfg.go` flag-set pattern plus standard OTel SDK env vars
(`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, etc.).

### What gets emitted

* **Span names** are business-operation names defined in `AGENTS.md`:
  `create_link`, `resolve_link`, `list_links`, `redirect`, `reject_request`,
  `storage.lookup`, `storage.write`, `storage.list`. These are emitted via
  the OTel auto-instrumentation libraries (`otelhttp`, `otelgrpc`) with span-name
  formatters that rename the default method/path names. HTTP server spans
  include the matched `http.route`; rejected methods are renamed to
  `reject_request` rather than being reported as redirects.
* **HTTP connection correlation** is available on traces through the
  `x40.link.server.connection.id` attribute. The value identifies requests
  multiplexed over one process-local TCP connection and is intentionally
  excluded from metrics because it is high cardinality. Combine it with the
  Cloud Run `faas.instance` resource attribute when comparing instances.
* **Custom business metrics** under the `x40.link` namespace:
  `links.created`, `links.resolved`, `links.not_found`,
  `storage.errors`. The first three carry a `storage` label
  (`boltdb`, `hashmap`, `yaml`, `firestore`) and a `surface` label
  (`http`, `grpc`); the last carries `storage` and `op` (`lookup`,
  `write`).
* **Go runtime metrics** (`process.runtime.go.*`) and host metrics.
* **HTTP / gRPC semantic-convention metrics** (`http.server.request.duration`,
  `rpc.server.duration`, etc.) from the contrib libraries.

### Local development

The default OTLP endpoint is `telemetry.googleapis.com:443`. To send
data to a local collector instead, set the standard OTel env var:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 ./x40.link serve \
    --storage.boltdb.file /tmp/x40.link.db
```

A runnable collector config is in `docs/content/how-to/observe-locally.md`.

### Production (Cloud Run)

The Cloud Run service account needs these IAM roles:

* `roles/telemetry.tracesWriter` — write traces to the Google
  Telemetry API (which feeds Cloud Trace).
* `roles/monitoring.metricWriter` — write metrics to Cloud
  Monitoring.
* `roles/serviceusage.serviceUsageConsumer` — consume the Telemetry
  API in the project.

These are needed because the OTel SDK's OTLP exporter
targets the unified **Google Telemetry API** at
`telemetry.googleapis.com:443` (not the legacy Cloud Trace v2
endpoint at `cloudtrace.googleapis.com`, which only speaks the
non-OTLP `google.devtools.cloudtrace.v2` protobuf).

Application Default Credentials (via the Cloud Run metadata
server) authenticate the exporter. The OTLP/gRPC client uses
`grpc.WithPerRPCCredentials(oauth.NewApplicationDefault(...))`
to inject per-RPC OAuth tokens with the
`https://www.googleapis.com/auth/trace.append` (traces) and
`https://www.googleapis.com/auth/monitoring.write` (metrics)
scopes. The token source is created lazily — no token fetch
happens until the first export attempt.

The `gcp.NewDetector()` resource detector automatically populates
Cloud Run-specific attributes (project, region, service revision)
on the OTel Resource. The application also copies the detector's
`cloud.account.id` value to `gcp.project_id`, which the Google
Telemetry API requires. The detector only runs when the `K_SERVICE`
env var is set, which is the standard Cloud Run signal.

Terraform enables `telemetry.googleapis.com`,
`cloudtrace.googleapis.com`, `monitoring.googleapis.com`, and
`logging.googleapis.com`. It provisions the dedicated
`x40-link-runtime` service account and grants the telemetry roles above,
plus `roles/datastore.user` for Firestore. Apply the production
Terraform before deploying the Cloud Run manifest, because the manifest
references that service account.

#### Deployment patterns

The `otel.exporter.endpoint` flag (and the `OTEL_EXPORTER_OTLP_ENDPOINT`
env var, which takes precedence) controls where the OTLP/gRPC exporter
sends data. The standard OTel `OTEL_EXPORTER_OTLP_ENDPOINT` env var
takes precedence over the flag. Two patterns are supported:

**Direct export to the Google Telemetry API (default)** — The
application dials `telemetry.googleapis.com:443` with TLS and
per-RPC OAuth via Application Default Credentials. This is the
standard out-of-the-box configuration and is what the flags
default to. The service account above carries the IAM roles.

* Endpoint: `telemetry.googleapis.com:443` (the default)
* `--otel.exporter.insecure` (default `false`) — TLS
* Credentials: ADC; no extra setup required inside Cloud Run

**Sidecar collector** — The OTel Collector runs as a
[Cloud Run sidecar container](https://cloud.google.com/run/docs/deploying#sidecars)
on the same instance as `x40.link`, listening on `localhost:4317`
over plaintext. The sidecar forwards to Google Telemetry API
with TLS over Google's internal network. Set both via the OTel
env vars:

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
```

The `http` scheme selects plaintext transport. The equivalent flags are
`--otel.exporter.endpoint=localhost:4317` and
`--otel.exporter.insecure=true`.

The sidecar pattern is recommended for high-volume workloads: it
provides buffering, retries, and queueing between the application
and Google's API, and lets you swap out the backend without
re-deploying the application. When this pattern is in use the
`otlpCredentials` plumbing in `otel/init.go` is short-circuited
because the endpoint is not on `googleapis.com`; the sidecar
handles its own auth to Google.

#### IPv4-only dialing

The OTLP/gRPC dialer is unconditionally forced to IPv4 (via a
custom `grpc.WithContextDialer`). Cloud Run's network egress —
both Serverless VPC Access and Direct VPC Egress — historically
terminates the IPv6 path; Go's default DNS resolver (Happy
Eyeballs, RFC 6555) prefers the AAAA record and the dial hangs
until the per-attempt deadline expires before the IPv4 fallback
completes. Forcing IPv4 short-circuits the IPv6 attempt. See
`otel.init.ipv4Dialer` for the implementation. No configuration
is required; the dialer is unconditionally applied.

The `service.version` resource attribute is set from `version.Version`,
which is overridden at link time via:

```
go build -ldflags "-X github.com/andrewhowdencom/x40.link/version.Version=<value>" main.go
```

The `task bin/*` build commands and the `Containerfile` both inject this
flag. When unset, the value is `"unknown"`.
