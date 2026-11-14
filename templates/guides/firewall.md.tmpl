---
page_title: "Firewall adoption and recovery"
subcategory: ""
description: "Adopt selected-profile firewall policy and handle interrupted activation."
---

# Firewall adoption and recovery

## Adopt the current firewall

1. Define an empty `synology_firewall` resource block to keep the policy DSM reports.
2. Add the resource’s import block with ID `firewall`.
3. Run `terraform plan`. Apply only when it reports `1 to import, 0 to add, 0 to change, 0 to destroy`.

Import reads the profile without saving it, activating it, or stopping a task. Destroy releases Terraform management and leaves the policy installed.

## Change the selected profile

1. Verify an independent management path before editing rules. Use a direct certificate-valid DSM endpoint on DSM's native HTTPS port. Verify the actual connection path as well as the port.
2. When editing rules, start from the complete adapter policy DSM reports. For an enabled policy, keep DSM port checking enabled and set `management_networks` to the administrator source CIDRs DSM sees after NAT or VPN translation.
3. For each management network, place an explicit global allow for the DSM HTTPS port before matching denies or uncertain selectors. The provider refuses early GeoIP, service-name, source-port or partial-range selectors when it cannot prove management access.
4. Review the rules and their order in the plan.
5. Apply the reviewed change.
6. Verify DSM access from the intended networks.

The resource owns the complete selected profile and can select an existing one. Use DSM to create, rename, or delete profiles. Rules are evaluated in order. Omitted policy preserves the current settings. A response containing unknown rule fields stops the operation. The provider confirms activation through the DSM task result and a readback of stored configuration. Kernel changes and unapplied edits can escape drift detection. Keep the recovery path available through the change.

## Recover an interrupted apply

Inspect DSM through the independent path before planning again. Refresh can poll an acknowledged activation using the task ID saved in Terraform private state. A saved profile by itself does not confirm activation. A process exit or lost RPC response can leave state without the task ID.

If the write is unacknowledged or DSM shows a stored policy with activation still uncertain:

1. Back up Terraform state and check the selected profile, running tasks and effective access in DSM.
2. Restore access if needed. Confirm which policy is active before proceeding.
3. Replace the resource and its import block with a `removed` block that sets `destroy = false`. Apply a plan containing only the management release.
4. Replace the `removed` block with the resource and import block using the confirmed policy. Apply an import-only plan.

Recovery requires inspecting DSM. Expired task history cannot be reconstructed, and DSM does not provide an atomic rollback for this workflow.

The API contract follows the shipped DSM 7.1.1 UI and [Synology firewall documentation](https://kb.synology.com/en-global/DSM/help/DSM/AdminCenter/connection_security_firewall). Hardware mutation and lockout recovery remain unverified without a disposable DSM environment.
