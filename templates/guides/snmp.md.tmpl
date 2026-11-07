---
page_title: "SNMP adoption and secrets"
subcategory: ""
description: |-
  Adopt existing SNMP configuration without changing secrets.
---

# SNMP adoption and secrets

Import `synology_snmp` with ID `snmp` before changing an existing NAS. Import and refresh do not write configuration. Start with the observed service flags and omit all secret arguments and versions. An adoption plan must contain the import and no configuration changes.

The community and v3 authentication/privacy passwords are write-only. Supply the corresponding `_wo` input and increase its `_version` to change it. Keep the last applied version in configuration. Import initializes each version to zero; these counters do not detect external secret changes. Even though DSM can return the community, the provider uses it only in memory to preserve it during unrelated updates and never saves it in state. System name, location, contact and v3 username are configuration metadata and do enter state.

Enabling v1/v2 requires an existing nonempty community or a new community with an increased version. Enabling v3, changing its username or changing its authentication protocol requires an authentication password and an increased version. Enabling privacy or changing its username/protocol requires a privacy password and an increased version. Secrets must satisfy the DSM UI's ASCII character rules: community and username are 1–64 characters; v3 passwords are 8–127 characters. Spaces and backslashes are excluded.

Configure `auth_protocol` only with an enabled v3 service, and `privacy_protocol` only with enabled v3 privacy. DSM can omit these fields while inactive; omission is represented as null, not an invented default. A missing active field is an error. Disable privacy explicitly when disabling v3. Secret rotations for inactive protocols are rejected rather than checkpointed as successful.

An acknowledged write checkpoints secret versions before readback. A later readback error does not cause an acknowledged rotation to be resent on the next apply. A transport failure during the write cannot establish whether DSM accepted it: inspect DSM and refresh before retrying; do not blindly replay an old saved plan. No cross-client transaction or automatic rollback is promised. Use one configuration writer.

Read and import compatibility are verified separately from mutation compatibility. Synthetic HTTPS tests cover lifecycle, drift, omitted metadata preservation, secret non-persistence and acknowledged-write/readback failure. Real DSM writes remain experimental until exercised on an explicitly authorized disposable environment. Destroy only releases Terraform management and leaves SNMP settings unchanged.
