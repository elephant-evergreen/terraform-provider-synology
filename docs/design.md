# Design and dependencies

The provider communicates directly with DSM over verified HTTPS. Terraform owns ordering, state, import, and planning. The provider owns typed configuration, DSM API adaptation, readback, identity, cancellation, redaction, and safe failure behavior. There is no remote agent, custom CLI, shell transport, or generic API resource.

Existing objects are adopted through import, never a create operation. Read and discovery must reflect DSM rather than echo the previous Terraform state. A failed read does not establish absence. Unreadable secrets use write-only inputs and explicit generations; they are not presented as drift-detectable.

Shared folders, built-in accounts, and management access have independent protection boundaries. Changes must preserve data, unknown API fields, unmanaged principals, and settings outside the resource's schema. Configuration backup is not a backup of user files. DSM does not offer a demonstrated transaction spanning these APIs; the provider cannot promise cross-resource atomicity or automatic restoration of data.

Initial compatibility work targets DSM 7.1.1 on DS220j. No hardware or lifecycle compatibility is claimed until the corresponding evidence is recorded. Storage pools, RAID, volume recreation, user-file contents, firmware installation, and execution of backups remain outside configuration lifecycle management.

## Dependencies

Go 1.27.3 and its standard HTTP, TLS, JSON and URL implementations provide transport and parsing. HashiCorp's Plugin Framework v1.19.0, Plugin Go v0.31.0 and Plugin Testing v1.16.0 provide the official Terraform protocol, schemas and acceptance harness. The versions match the existing router provider's verified toolchain. gRPC v1.83.2 retains the router provider's vulnerability fix until the Framework selects a fixed version itself. Go module checksums and vulnerability checks are required before release.

The existing synology-community/synology v0.6.11 implementation does not provide the required share and account resources and contains incomplete import and refresh behavior. batonogov/synology-dsm v0.8.1 provides useful API research, but targets newer DSM versions and does not establish the required lifecycle and safety guarantees for this deployment. A narrow, original typed adapter avoids adopting either provider's transport and resource semantics. This also makes DSM compatibility and maintenance the responsibility of this project.

References: [Synology File Station API](https://global.download.synology.com/download/Document/Software/DeveloperGuide/Package/FileStation/All/enu/Synology_File_Station_API_Guide.pdf), [HashiCorp resource design](https://developer.hashicorp.com/terraform/plugin/best-practices/hashicorp-provider-design-principles), [resource import](https://developer.hashicorp.com/terraform/plugin/framework/resources/import), [write-only arguments](https://developer.hashicorp.com/terraform/plugin/framework/resources/write-only-arguments), [API research at v0.8.1](https://github.com/batonogov/terraform-provider-synology-dsm/tree/v0.8.1/internal/client).
