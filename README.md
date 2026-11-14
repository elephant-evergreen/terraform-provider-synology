# Terraform Provider for Synology

Manage file shares, local accounts, permissions, and supported Synology DSM services over verified HTTPS. Check the [verified DSM versions and operations](docs/compatibility.md) before adopting a NAS. Live writes remain experimental. See the [Terraform Registry reference](https://registry.terraform.io/providers/elephant-evergreen/synology/latest/docs) and read the [release notes](https://github.com/elephant-evergreen/terraform-provider-synology/releases) before changing versions.

## Check the NAS connection

Follow [Check the NAS connection in Terraform Registry](https://registry.terraform.io/providers/elephant-evergreen/synology/latest/docs#check-the-nas-connection) to connect over verified HTTPS and read the Ethernet interfaces.

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
