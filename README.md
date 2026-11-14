# Terraform Provider for Synology

Manage file shares, local accounts, permissions, and supported Synology DSM services over verified HTTPS. Check the [verified DSM versions and operations](docs/compatibility.md) before adopting a NAS. Live writes remain experimental. See the [Terraform Registry reference](https://registry.terraform.io/providers/elephant-evergreen/synology/latest/docs) and read the [release notes](https://github.com/elephant-evergreen/terraform-provider-synology/releases) before changing versions.

## Check the NAS connection

Set `SYNOLOGY_ENDPOINT` to the DSM HTTPS origin and supply `SYNOLOGY_USERNAME` and `SYNOLOGY_PASSWORD` through your usual secret management. If DSM uses a private certificate authority, set `SYNOLOGY_CA_FILE` to its PEM bundle.

Save this as `main.tf` in an empty directory. The example reads physical network interfaces without changing DSM settings.

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

output "interfaces" {
  value = sort(keys(data.synology_network_interfaces.nas.interfaces))
}
```

1. Run `terraform init` to install the provider.
2. Run `terraform plan` to read the NAS.
3. Look for your Ethernet interface names, such as `eth0`, in the planned `interfaces` output.

Keep `.terraform.lock.hcl` in version control. To pick up a compatible patch, read its release notes, run `terraform init -upgrade`, and review the lockfile and plan.

## Adopt an existing NAS

Start with the **Adopt existing settings** section on the Registry page for your share, user, group, or service. For one object, plan for `1 to import, 0 to add, 0 to change, 0 to destroy` before applying. After the import, run `terraform plan` again and expect `No changes` if your configuration matches DSM. Import leaves DSM configuration and user files intact. Passwords and transient sessions stay out of state. Shared folders, users, and groups are protected against deletion by default. Destroying a service or share-permission resource releases Terraform management and leaves the DSM settings in place.

Use the guide for the operation you want to perform:

- [Change network, interface, or proxy settings](docs/guides/network.md)
- [Change firewall rules and preserve management access](docs/guides/firewall.md)
- [Rotate SNMP secrets](docs/guides/snmp.md)
- [Enable or disable user homes](docs/guides/user_homes.md)
- [Manage QuickConnect](docs/guides/quickconnect.md), [DDNS](docs/guides/ddns.md), or [packages](docs/guides/packages.md)

Configuration backup is a read-only data source until recovery-key delivery is established. Storage reconstruction, file contents, backup execution, recovery and interactive account/application workflows remain outside the configuration lifecycle.

## Development

```sh
mise install
mise run check
```

Tests use local HTTPS fixtures with synthetic data. Ordinary CI has no NAS credentials. See [contributing](CONTRIBUTING.md), [design](docs/design.md) and [release procedure](docs/releases.md).

## License

[MPL-2.0](LICENSE). This project is not affiliated with or endorsed by Synology or HashiCorp.
