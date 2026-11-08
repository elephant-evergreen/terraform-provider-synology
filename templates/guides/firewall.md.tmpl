---
page_title: "Firewall adoption and recovery"
subcategory: ""
description: "Adopt selected-profile firewall policy and handle interrupted activation."
---

# Firewall adoption and recovery

Import `synology_firewall` with ID `firewall`. Pin the observed enabled setting, profile name, port-check policy and complete adapter map. Review an import-only plan with zero changes before applying. Do not copy the example policy over an existing profile. Profile creation, rename and deletion remain outside this singleton. Destroy only releases management.

Rule list order is priority. Each rule has a typed action, protocol, source and port selector; no raw API or JSON configuration is accepted. Omission preserves the current policy. Responses with unknown rule fields fail instead of silently dropping them. Profile storage and activation are separate: the provider verifies complete stored readback, then starts activation and polls its acknowledged task. A successful save alone is never treated as activation. Read and import never activate a profile or stop a task.

Before an enabled-policy change, declare the administrator source CIDRs actually seen by DSM, including NAT/VPN translation, in `management_networks`. Every declared network needs an explicit global allow for the observed DSM HTTPS port before any matching deny or uncertain selector. The guard conservatively refuses GeoIP, service-name, source-port or partial-range selectors when it cannot prove that allowance. It does not broaden the policy automatically. DSM port checking must remain enabled. Use a direct certificate-valid DSM endpoint whose port matches DSM's native HTTPS port; matching numbers do not establish the absence of an intermediary. These checks do not verify the asserted source networks or replace an independent recovery path.

The resource reads stored profiles, selected profile, enabled state and port-check policy. DSM's demonstrated API does not expose a comparable kernel-rule digest. Unapplied out-of-band edits and direct kernel changes cannot be fully drift-detected. Provider-initiated activation is confirmed only through the reported task result and configuration readback. No atomic rollback or automatic recovery from lockout is promised.

A failed multi-step change retains its target and acknowledged task ID in Terraform private state, which is configuration metadata requiring protected state storage. Refresh only polls an acknowledged activation and reads its target. Storage success followed by failed activation preparation remains uncertain and cannot clear the guard merely because stored fields match. An unacknowledged write requires explicit reconciliation: back up state, inspect DSM profiles, running tasks and effective access through an independent path, restore access if needed, then use a removed block with `destroy = false` and import the confirmed policy. Do not delete profiles to repair Terraform state.

A process exit or lost Terraform RPC response can lose pending state before Terraform saves it. Exactly-once execution and durable operation journaling across that boundary are not provided. After any interrupted apply inspect DSM before planning another change. A fresh baseline cannot reconstruct expired task history.

The API contract follows the shipped DSM 7.1.1 UI and [Synology firewall documentation](https://kb.synology.com/en-global/DSM/help/DSM/AdminCenter/connection_security_firewall). Hardware mutation and lockout recovery remain unverified without a disposable DSM environment.
