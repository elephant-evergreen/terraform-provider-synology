# Terraform Provider for Synology

Manage DSM shared folders, local accounts, groups, explicit share permissions, file-sharing services and time settings using typed Terraform resources and direct HTTPS communication.

The provider is under development. A GitHub repository or successful fixture test does not establish Registry availability or hardware lifecycle compatibility. See [design](docs/design.md) and [verification boundaries](docs/testing.md).

## Adopt an existing NAS

Existing objects must be imported. Import never creates a replacement, resets a password, changes a share permission or modifies user files. Shared folders, users, groups and permissions are protected against deletion by default.

The intended Registry source is `elephant-evergreen/synology`. Once a verified release is published, Terraform will install the provider using normal `terraform init`. Connection settings may be supplied through `SYNOLOGY_ENDPOINT`, `SYNOLOGY_USERNAME` and `SYNOLOGY_PASSWORD`, or ephemeral values in the provider configuration. The endpoint must have a valid HTTPS certificate; a private CA can be supplied with `ca_file`.

Use ordinary Terraform import blocks and review the resulting plan before applying it. An adoption plan must contain imports and zero creates, updates, replacements or deletions. Configuration and account information enter state; passwords and transient DSM sessions must not.

## Development

```sh
mise install
mise run check
```

Tests use local HTTPS fixtures with synthetic data. Ordinary CI has no NAS credentials. See [contributing](CONTRIBUTING.md), [design](docs/design.md) and [release procedure](docs/releases.md).

## License

[MPL-2.0](LICENSE). This project is not affiliated with or endorsed by Synology or HashiCorp.
