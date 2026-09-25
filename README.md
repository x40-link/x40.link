# @.link

[![Go Reference](https://pkg.go.dev/badge/github.com/andrewhowdencom/x40.link.svg)](https://pkg.go.dev/github.com/andrewhowdencom/x40.link)
 . [![build:x40.link](https://github.com/andrewhowdencom/x40.link/actions/workflows/main+x40.link.yml/badge.svg)](https://github.com/andrewhowdencom/x40.link/actions/workflows/main+x40.link.yml)
 · [![build:documentation](https://github.com/andrewhowdencom/x40.link/actions/workflows/main+documentation.yml/badge.svg)](https://github.com/andrewhowdencom/x40.link/actions/workflows/main+documentation.yml)

The short link service. Named `x40`, it represents the hex for the "@" character. Created as firebase is [going away]. 
Learn more via the [documentation]

[going away]: https://firebase.google.com/support/dynamic-links-faq
[documentation]: https://www.x40.dev

## Usage

Probably, do not, at this point. I'm working on this project instead of full-time work; however, the stability of this
project is shaky at best. I'm stabilizing this solution's network APIs and client implementations
before the code solution is even reasonably stable.

### Command-Line

Download `x40-cli_<os>_<arch>.tar.gz` from the
[GitHub releases](https://github.com/andrewhowdencom/x40.link/releases) page.
Archives are available for Linux (`amd64`, `arm64`, `arm` / ARMv7), macOS
(`darwin`, `amd64` or `arm64`), and Windows (`amd64`, `arm64`). Extract the
archive and put `@` (Windows: `@.exe`) in a directory on your `PATH`.
Each release includes `SHA256SUMS` to verify the downloaded archives.

The CLI binary supports four operations.

**Create a short link** (`@`):

```bash
@ https://my.destination.url/path
@ source.domain https://my.destination.url/path
@ https://source.domain/path https://my.destination.url/path
```

The first form generates a random short URL on the default domain. The second generates a random path
on a registered domain. The third lets you supply the short URL explicitly. This operation requires
OAuth credentials.

On Firestore, creation claims an address atomically. Creating an existing
address returns an error and preserves its destination. Escaped source paths
are preserved: `/foo/bar`, `/foo+bar`, and `/foo%2Fbar` are distinct links.
Existing Firestore installations must follow the
[path-key migration guide](docs/content/how-to/migrate-firestore-path-keys.md)
before deploying this version.

**Log in with a different account** (`@ login`):

```bash
@ login
```

Always starts a fresh OAuth device authorization flow. The CLI replaces its cached credentials only
after authentication succeeds, so a failed or cancelled login leaves the existing account available.
After upgrading from a version that asked you to log in repeatedly, run `@ login` once to obtain a
refresh token. The CLI then renews expired access tokens without another device login while that
refresh token remains valid.
See [Change the CLI account](docs/content/how-to/change-cli-account.md) for the complete workflow.

**Look up a short link's destination** (`@ resolve`):

```bash
@ resolve https://source.domain/path
```

Prints the URL the short link redirects to. The destination of a short link is functionally public
information (the HTTP redirect already discloses it to anonymous users), so this operation does
not require authentication.

**List links** (`@ list`):

```bash
@ list
@ list --domain source.domain
```

Prints each short URL and destination in aligned columns. Authentication is
required. Firestore lists links owned by the signed-in account; backends that
do not store owners list all links. `--domain` filters by the short URL's
domain. The current API returns all matching links in one response. Each
Firestore list reads the matching documents again, so repeated lists consume
read quota in proportion to the number of matching links.

## Understanding this work

This project functions as a demonstration of work. In the future, I will likely cannibalize it for the
[Practical Introduction to Observability](h4n.link/pito). In it, you can see:

**📈 Algorithmic Complexity**

You'll see different implementations of the same problem —
[finding a URL in a set](https://github.com/andrewhowdencom/x40.link/tree/main/storage/memory). There is the
naive, [linear implementation](https://github.com/andrewhowdencom/x40.link/blob/main/storage/memory/linear_search.go),
a [binary search implementation](https://github.com/andrewhowdencom/x40.link/blob/main/storage/memory/binary_search.go),
and the one we'd
actually use — a very [simple hashmap](https://github.com/andrewhowdencom/x40.link/blob/main/storage/memory/hash_table.go).
There are even benchmarks to
[validate their performance!](https://github.com/andrewhowdencom/x40.link/blob/main/storage/storage_test.go#L87-L149)

**📖 Documentation**

Learn more about the project, including how to solve specific problems, through the documentation website at
[x40.dev](https://www.x40.dev). This documentation follows the goal-oriented
[Divio documentation structure](https://documentation.divio.com/) to help users of the documentation find what
they're looking for quickly, as well as allow developers to add documentation in a structured way that does
not become a mess over time.

**♾️ Functional Arguments**

Look at [the BoltDB-backed URL storage](https://github.com/andrewhowdencom/x40.link/blob/main/storage/boltdb/boltdb.go#L26)
and see the variadic argument approach [popularized by Dave Cheany](https://dave.cheney.net/2014/10/17/functional-options-for-friendly-apis)
and in broad use at [Uber](https://github.com/uber-go/guide/blob/master/style.md#functional-options).

**✍️ Helpful commit messages**

Read the [commit history to understand my thinking](https://github.com/andrewhowdencom/x40.link/commits/main/) while
writing each unit of work. You can see how the thinking has changed over time! You can read more
[about why I think this is so important](https://medium.com/@andrewhowdencom/anatomy-of-a-good-commit-message-acd9c4490437)

**🚆 Infrastructure as Code**

Look at the [infrastructure as code](https://github.com/andrewhowdencom/x40.link/tree/main/deploy/prod/tf) definitions,
using the [Tofu](https://opentofu.org/) (or open-source Terraform implementation) infrastructure tool to
[create DNS records](https://github.com/andrewhowdencom/x40.link/blob/main/deploy/prod/tf/cr.tf). See how it is
[configured to store its state in Google Cloud.](https://github.com/andrewhowdencom/x40.link/blob/main/deploy/prod/tf/state.tf)

You can also see how we pack the application [into a container](https://github.com/andrewhowdencom/x40.link/blob/main/Containerfile)
using a multi-step build [via the open-source Podman application](https://github.com/andrewhowdencom/x40.link/blob/main/Taskfile.yml#L75-L98).
This is built every time we
[merge to main](https://github.com/andrewhowdencom/x40.link/blob/main/.github/workflows/main%2Bx40.link.yml) and store both
in [GitHub](https://github.com/andrewhowdencom?tab=packages&repo_name=x40.link) and Google Cloud.

You can check the infrastructure by visiting the managed domains with:

* **docs**: https://x40.dev
* **app**: https://x40.link
* **short links**: https://0x5e.link

**🤖 Task Runner**

Run tasks via the [Taskfile](https://github.com/andrewhowdencom/x40.link/blob/main/Taskfile.yml) from the excellent
[Task Files](https://taskfile.dev/) project and see how to build the application, including for different
operating systems. Get a better understanding of the available tasks via:

```bash
$ task --list
```

Command, or learn more about each task with:

```bash
$ task --summary <task>
```

**🧪 Test Driven Development**

Take a look around at the files
[suffixed with _test](https://github.com/search?q=repo%3Aandrewhowdencom%2Fx40.link+path%3A_test.go&type=code). You'll
see the popular "table-driven test" format, with many tests being invoked in parallel to ensure fast execution and concurrency safety.
You'll also see the occasional
[benchmark](https://github.com/search?q=repo%3Aandrewhowdencom%2Fx40.link+path%3A_test.go+Benchmark&type=code), as well
as well as some
[testable examples](https://github.com/search?q=repo%3Aandrewhowdencom%2Fx40.link+path%3A_test.go+Example&type=code).
You can learn more on [the go website](https://go.dev/blog/examples). Some tests even validate concurrency via
[the go race detector](https://go.dev/blog/race-detector) and `go test -race`!

### ❓ Remaining Work

Quite a bit of work remains in this project before it becomes "production-ready!" For example,

1. Observability instrumentation (logs and profiling; traces and metrics
   are in as of this writing)
1. Data Backups (e.g. Scheduled, Commit Logs and so on)
1. Service Level Management (e.g., SLOs, SLAs)

I have worked with all of these technologies before; however, I have only a limited number of daily hours! Perhaps
you can [email me](mailto:hello@andrewhowden.com), and I can help you find evidence of what you're looking for.


## Development & Deployment

See [the development documentation](DEVELOPMENT.md) or the [deployment documentation](DEPLOYMENT.md)
