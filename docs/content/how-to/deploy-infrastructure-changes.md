# Deploy Infrastructure Changes

## You will need

* The [project tools](../reference/code/requirements.md)
* (If accessing Google Cloud) Google Cloud's [default application configured](https://registry.terraform.io/providers/hashicorp/google/latest/docs/guides/provider_reference#running-terraform-on-your-workstation)
* (If accessing GitHub) A [personal access token](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)
  (with administrative rights to the repository)
* (If accessing Auth0) `AUTH0_DOMAIN` and an Auth0 API token or client credentials

## Environments

There are different environments that are controlled by Tofu. These include:

| Environment | Description                                                 |
|:------------|:------------------------------------------------------------|
| a0          | Auth0 API, CLI application, roles, and permissions          |
| gh          | The GitHub repository containing the project                |
| prod        | All resources deployed in Google Cloud, publicly accessible |

## Deploying Changes

The changes need to be deployed in two steps:

1. Plan the changes:

    ```bash
    task tofu/plan ENV=<environment>
    ```

2. CAREFULLY INSPECT the output, and ensure you understand it all
3. Apply the changes

    ```bash
    task tofu/apply ENV=<environment>
    ```

If an apply only partly succeeds, create a new plan before retrying. The
previous saved plan no longer describes the current infrastructure state.

## Activate shp7.link

The production configuration adds a `shp7-link` Cloud DNS zone, an apex A
record pointing to the shared load balancer, and a dedicated managed TLS
certificate attached to the existing HTTPS proxy. It covers `shp7.link`;
`www.shp7.link` is not configured.

1. Plan and apply the `prod` environment using the steps above.
2. Retrieve the nameservers assigned to the new zone:

    ```bash
    gcloud dns managed-zones describe shp7-link \
      --project=andrewhowdencom --format='value(nameServers)'
    ```

3. Set those nameservers at the domain registrar for `shp7.link`.
   Registrar delegation is managed outside this Terraform configuration.
4. Wait for DNS propagation and for the certificate to become `ACTIVE`:

    ```bash
    gcloud compute ssl-certificates describe shp7-link \
      --project=andrewhowdencom --global --format='value(managed.status)'
    ```

5. Create a short link on `shp7.link` and open its HTTPS URL to verify the
   redirect. The domain root only redirects if a link has been created for
   that path.
