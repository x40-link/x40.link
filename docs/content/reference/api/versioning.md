# Versioning

The API provides multiple different API versions, which indicate what you should expect (in terms of stability)

The current management contract is `x40.link.v1alpha.ShortLinkService`.
Its source proto and generated clients live in the separate
[`x40-link/api`](https://github.com/x40-link/api) repository. Both gRPC and
HTTP/JSON routes use this version; HTTP routes begin with `/v1alpha/`.

* **v1alpha**: Under testing. Breaking changes may be made as the contract evolves.
* **v1beta**: Early consumer use. Breaking changes are avoided where possible.
* **v1**: Stable public contract. Breaking changes require a new major version.
