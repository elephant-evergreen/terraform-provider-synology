# Terraform Provider for Synology

Manage file shares, local accounts, permissions, and supported Synology DSM services over verified HTTPS. Check the [verified DSM versions and operations](docs/compatibility.md) before adopting a NAS. Live writes remain experimental. See the [Terraform Registry reference](https://registry.terraform.io/providers/elephant-evergreen/synology/latest/docs) and read the [release notes](https://github.com/elephant-evergreen/terraform-provider-synology/releases) before changing versions.

## Check the NAS connection

Set `SYNOLOGY_ENDPOINT` to the DSM HTTPS origin and supply `SYNOLOGY_USERNAME` and `SYNOLOGY_PASSWORD` through your usual secret management. If DSM uses a private certificate authority, set `SYNOLOGY_CA_FILE` to its PEM bundle.

Save the following as `main.tf` in an empty directory, then run `terraform init` and `terraform plan`. The data source reads physical network interfaces without changing DSM settings.

```hcl
terraform {
  required_version = ">= 1.17.1"

  required_providers {
    synology = {
      source  = "elephant-evergreen/synology"
      version = "~> 0.4.0"
    }
  }
}

provider "synology" {}

data "synology_network_interfaces" "nas" {}
```

Keep `.terraform.lock.hcl` in version control. To pick up a compatible patch, read its release notes, run `terraform init -upgrade`, and review the lockfile and plan.

## Adopt an existing NAS

Use Terraform import blocks for existing shares, users, groups, or service settings. Review an import-only plan with zero creates, updates, replacements, or deletions. Import leaves DSM configuration and user files intact. Passwords and transient sessions stay out of state. Shared folders, users, and groups are protected against deletion by default. Destroying a service or share-permission resource releases Terraform management and leaves the DSM settings in place.

For SNMP adoption and explicit secret changes, see the [SNMP guide](docs/guides/snmp.md). Import leaves community and v3 passwords unchanged; unreadable secrets cannot be drift-detected. Applied SNMP secret versions protect their state from Destroy; release management explicitly with a removed block using destroy = false.

User-home configuration requires import and retains interrupted background operations. See the [user-home guide](docs/guides/user_homes.md) before enabling or disabling homes. Existing home-folder relocation remains a separate data-migration workflow.

For network, firewall, DDNS and package adoption, use the [network](docs/guides/network.md), [firewall](docs/guides/firewall.md), [QuickConnect](docs/guides/quickconnect.md), [DDNS](docs/guides/ddns.md) and [package](docs/guides/packages.md) guides. Configuration backup is a read-only data source until recovery-key delivery is established. Storage reconstruction, file contents, backup execution, recovery and interactive account/application workflows remain outside the configuration lifecycle.

## Development

```sh
mise install
mise run check
```

Tests use local HTTPS fixtures with synthetic data. Ordinary CI has no NAS credentials. See [contributing](CONTRIBUTING.md), [design](docs/design.md) and [release procedure](docs/releases.md).

## License

[MPL-2.0](LICENSE). This project is not affiliated with or endorsed by Synology or HashiCorp.
