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
RELEASE_VERSION=v1.2.3 task release/cli/test
```

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
* **`@ resolve <url>`** — look up the destination of a short link. Requires
  OAuth credentials. See `cli/main.go::DoResolve` and
  `cli/main.go::doResolveWithClient`.
* **`@ list [--domain <host>]`** — list owned short URLs and destinations.
  Requires OAuth credentials and follows all API pages. The domain filter
  applies to the short URL's host.

The flag sets are split into `apiFlagSet` (just `cfg.APIEndpoint`) and
`authFlagSet` (the OAuth-related flags). The root command uses both
(composed into `urlFlagSet`), `login` uses only `authFlagSet`, and `resolve`
and `list` use both. Adding a new subcommand with a
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
The token cache warns on stderr when an expired access token has no refresh
token and starts device login. Device authorization also warns if the provider
returns a token without refresh access, so the next expiry is diagnosable at
login time. These warnings contain no token values.

## Management API and Authentication

The canonical proto and generated Go clients live in
[`x40-link/api`](https://github.com/x40-link/api). `go.mod` pins the version
used here. This repository implements `x40.link.v1alpha.ShortLinkService`
in `api/management/` and serves the five methods over gRPC and generated
HTTP/JSON routes under `/v1alpha/`. There is no local proto generation task.

Each method declares an `oauth2_scope` in the canonical proto. `api/api.go`
extracts those scopes for the JWT interceptor; the HTTP gateway applies the
same authorizer before invoking the service. All management methods require
authentication, including `GetShortLink`. The public redirect remains
anonymous. The CLI requests all five scopes plus `offline_access`; users
with tokens issued for the former `ManageURLs` scopes must run `@ login`.

Resource names use `domains/{domain}/shortLinks/p{base32_path}`. Domains
are lowercase IDNA ASCII. Paths are escaped absolute URI paths; `/foo/bar`,
`/foo+bar`, `/foo%2Fbar`, and `/foo//bar` are distinct. The final component
uses lowercase, unpadded RFC 4648 Base32 of the canonical escaped path.
Firestore continues to store documents under
`links/<domain>/shortLinks/p-<base32_path>`; the domain and path select the
same document for both redirects and management.

`ManagedStore` enforces ownership, atomic address claims, ETag conditional
writes, and 24-hour create request deduplication. Firestore, BoltDB, and the
in-memory backend implement it. The YAML backend remains redirect-only.
Firestore reads existing owned `shortLinks` documents into the new resource
model and preserves all existing redirects. Migrate older Firestore path keys
before deployment using [Migrate Firestore path keys](docs/content/how-to/migrate-firestore-path-keys.md).
The all-domain owner lookup needs the `shortLinks` collection-group index
defined in `deploy/prod/tf/firestore.tf`.

`ListShortLinks` defaults to 50 results, caps a page at 500, and returns an
opaque name-order cursor bound to the caller, parent, and requested page
size. The CLI follows all pages with a 30-second overall deadline. Backends
currently read all matching records to form each page; large lists can
consume substantial Firestore read quota. The Cloud Run request timeout is
60 seconds in `deploy/prod/cr/service.yaml`.

The service renames the existing request span to `create_link`, `get_link`,
`list_links`, `update_link`, or `delete_link`. Firestore operations annotate
the request span with `x40.storage.key_version=base32-v1` and
`db.system=firestore`.

## Observability

The server emits OpenTelemetry traces and metrics. The pipeline is
initialised in `otel.Init`, called from `cmd/serve.go::RunServe` before
the server starts listening. The OTel SDK is configured via the existing
`cfg/cfg.go` flag-set pattern plus standard OTel SDK env vars
(`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, etc.).

### What gets emitted

* **Span names** for management calls are `create_link`, `get_link`,
  `list_links`, `update_link`, and `delete_link`. The service renames the
  existing gRPC or HTTP request span. Redirects use `redirect`; rejected
  HTTP methods use `reject_request`. Existing storage operations use
  `storage.lookup`, `storage.write`, or `storage.list`. HTTP server spans
  include the matched `http.route`.
* **HTTP connection correlation** is available on traces through the
  `x40.link.server.connection.id` attribute. The value identifies requests
  multiplexed over one process-local TCP connection and is intentionally
  excluded from metrics because it is high cardinality. Combine it with the
  Cloud Run `faas.instance` resource attribute when comparing instances.
* **Redirect metrics** under the `x40.link` namespace include
  `links.resolved`, `links.not_found`, and `storage.errors`, labelled by
  storage backend. Management calls use request tracing and standard
  HTTP/gRPC duration metrics.
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
