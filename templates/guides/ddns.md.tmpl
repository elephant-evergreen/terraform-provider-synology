---
page_title: "DDNS records"
subcategory: ""
description: "Discover, import and configure DSM DDNS records without hidden account enrollment or certificate changes."
---

# DDNS records

## Find and import an existing record

Read `data.synology_ddns` to discover the available provider IDs and configured record IDs. The provider catalogue is a set: identical repeated catalogue rows are deduplicated. Duplicate configured record identities remain errors. Custom provider URLs and embedded credentials are never returned. `ExampleDNS` in examples is synthetic; choose an ID from the discovered catalogue.

Import existing records by their DSM record ID and set `provider_id` to the observed provider. Omit `password_wo` and keep `password_version = 0` during adoption. Create refuses an already configured provider. DSM supports one configured record per provider.

## Create a record or change credentials

New generic-provider records require an explicit hostname and credential generation. Passwords and API keys are write-only; increasing the generation intentionally rotates them. Username changes also require an explicit generation. Synology/TWNIC registration or renaming, browser consent, certificate issuance and account enrollment remain separate DSM workflows; their existing policies can be imported. Synology account authentication is not rotated through a DDNS password input.

Automatic public-address and interface-address observations are readable. Declare an address only with an empty corresponding interface selector for manual addressing. `default` requests public-address discovery, `disabled` disables that family and an interface name selects an existing interface. Omitted selectors and automatic observations are retained. The provider follows the vendor configuration-set and update-address sequence; it owns configuration, not DNS propagation or external-provider availability.

## Recover an interrupted write

An acknowledged credential generation is retained before readback. An unacknowledged creation or update blocks automatic adoption, replacement and retries. Back up state, inspect DSM and the external registration, explicitly release management with a removed block using `destroy = false`, then import the confirmed record without write-only credentials and with generation zero. Terraform may taint failed creation; do not accept replacement as recovery. A lost Terraform RPC response can lose pending metadata before Terraform saves it; inspect the systems after any interrupted apply.

## Remove a record

Deletion is protected by default and can unregister an external hostname. Disable protection in a separate reviewed apply only when removal is intended. An uncertain deletion is not blindly repeated: refresh can confirm absence, while a still-present record requires reconciliation. Releasing management with `destroy = false` never unregisters or disables DDNS. DSM does not provide a demonstrated incarnation UUID for this provider-slot identity; an identical out-of-band recreation cannot be distinguished from the same logical record.
