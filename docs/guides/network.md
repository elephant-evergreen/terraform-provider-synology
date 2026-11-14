---
page_title: "Network and proxy adoption"
subcategory: ""
description: "Adopt DSM network settings without changing connectivity or credentials."
---

# Network and proxy adoption

## Adopt network or proxy settings

Import `synology_network` with ID `network` and `synology_network_proxy` with ID `network_proxy`. Match the observed policy before applying. An adoption plan must contain imports and zero configuration changes. General network policy owns hostname, DNS and routing preference flags; interface addresses, DHCP leases and default routes are separate. Keep DHCP reservations in the router configuration. Do not apply DSM connectivity changes and router changes together; retain an independent management path and a certificate-valid endpoint.

Omitted fields preserve existing policy. Changing only the hostname does not rewrite DNS. Changing the DNS policy retains omitted DNS fields from a fresh read. Time Machine owns the complete set of advertised shared-folder names, never their contents. Configure service and shared-folder references to establish Terraform ordering.

Proxy passwords are write-only. Import with `password_version = 0` and omit `password_wo` to preserve existing credentials. To intentionally change one, increase the version and supply an ephemeral password. Enabling authentication or changing its username requires an explicit password generation. When different HTTPS proxy use is disabled, HTTP host and port also determine HTTPS settings; conflicting explicit HTTPS values are rejected.

## Recover an interrupted proxy password change

An acknowledged password generation is retained before readback. An unacknowledged secret write blocks Update and Destroy until explicit reconciliation. Back up state, inspect DSM, release management with a removed block using `destroy = false`, set the generation to zero and omit the write-only secret, then import the confirmed policy. Never reset NAS credentials to repair Terraform state. A lost Terraform RPC response can lose the operation baseline; inspect DSM before retrying an interrupted apply. Unreadable password drift cannot be detected. Private metadata and ordinary configuration still require protected state storage.

## Check the automatic backup policy

Automatic configuration backup is a read-only data source. Its setter can produce new recovery-key material; a complete key-delivery and recovery contract has not been established, so enable, rotation, account binding, backup execution and restore require a separate DSM recovery workflow. An enabled policy does not prove successful backup, and configuration backup does not back up shared-folder data.

## Adopt or change a physical Ethernet interface

Discover existing physical `ethN` slots with `data.synology_network_interfaces` and import each `synology_network_interface` by name. The resource binds the observed MAC identity. Hardware replacement or MAC changes require explicit reconciliation; refresh never silently adopts them. Create cannot create hardware or discard an interrupted baseline. Destroy releases management without disabling the interface.

IPv4 DHCP and native IPv6 `auto` observations remain computed when omitted. Do not configure lease addresses, masks or gateways while using DHCP, or static IPv6 fields in automatic/off mode. Changing to static policy requires explicitly declared address/prefix inputs and any default-route gateway; observed leases and router advertisements are not silently frozen. Static subnet/gateway, VLAN and MTU validation runs before writing. Coupled DNS is retained from a fresh read; general DNS has its own network policy. IPv4 and IPv6 setters remain separate, and partial success is not rolled back.

HA, NAT, blocked interfaces, bonds/virtual switches and IPv6 router/tunnel roles are not reset to make an update succeed. The resource refuses those role changes. Native IPv6 follows the vendor's `force` parameter only after refusing router/tunnel roles; no router configuration setter is called. Router VLAN/DHCP policy and DSM connectivity changes require separate applies and a working independent management path.

An acknowledged interface change resumes through read-only convergence. An uncertain acknowledgement blocks another write and management destruction until connectivity is restored and DSM is inspected. Back up state, release management with a removed block using `destroy = false`, and import the confirmed physical interface. No address probing, HTTP fallback, TLS bypass, remote shell or automatic network rollback is performed. A lost Terraform RPC response can lose pending metadata; inspect DSM after an interrupted apply.
