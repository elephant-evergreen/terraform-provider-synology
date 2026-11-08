---
page_title: "User-home adoption and interrupted operations"
subcategory: ""
description: |-
  Adopt existing user homes and retain interrupted operation identities.
---

# User-home adoption and interrupted operations

The DSM user-home service already exists. Import `synology_user_home` with ID `user_home`; an HCL import block supports adoption and an intentional update in one plan. A migration of a running NAS must first contain only the import, with zero configuration changes. Create refuses writes so replacement cannot discard the baseline of an earlier background operation.

Declare the observed local, domain and LDAP enabled flags. Omitted fields preserve their current DSM values. Domain and LDAP homes require local homes; disabling local homes must not implicitly disable dependent services. DSM feasibility restrictions and warnings must be resolved before applying. Personal Photo and remote home dependencies are checked before writing.

`location` selects a local volume such as `/volume1` when initially enabling homes. An established location cannot be changed by this resource because that would move user files. Perform any data migration through a separately reviewed procedure, then import the resulting configuration. Recycle-bin and encryption settings are observed only. An existing protected `synology_shared_folder` resource owns homes-share settings; encryption-key operations and file migration are outside this resource.

## Interrupted operations

Before a change, the provider discovers existing user-home background tasks and refuses to start another while one is running. It retains the target policy before the write, and checkpoints a returned task identity before waiting. An acknowledged operation resumes through read-only polling and readback. The pending goal and task identity use [provider private state](https://developer.hashicorp.com/terraform/plugin/framework/resources/private-state); they remain part of Terraform state and are not secret storage.

If acknowledgement is lost, refresh, Update and Destroy retain the reconciliation guard. A replacement cannot create a fresh operation baseline. No automatic retry, cancellation or rollback of the write is performed. Keep one configuration writer, retain protected state backups, and inspect DSM before reconciliation. A killed process or a lost Terraform RPC response can lose the pending record before Terraform saves it. Exactly-once execution is not guaranteed. After an interrupted apply, inspect DSM tasks and settings before planning again. Task discovery blocks another write while work is running, and a fresh read avoids resending an already-converged policy. Completed or expired task history is not a durable operation journal. These checks do not establish a distributed transaction.

To reconcile an unacknowledged operation:

1. Preserve the current state and verify the actual home-service and task status in DSM. Confirm that the operation has finished and existing data is intact.
2. Temporarily replace this resource and its import block with a Terraform `removed` block whose lifecycle has `destroy = false`. Review and apply a saved plan containing only that management release.
3. Restore the resource and import block using the observed policy. Review a new saved plan containing only the import and zero remote changes before applying it.

Destroy normally releases management without disabling homes or deleting files. It refuses to discard a pending operation. Shared-folder contents, home-folder relocation, backup execution and data restoration are separate workflows.

Synthetic HTTPS and Terraform tests establish import, update, drift, task completion, acknowledgement loss and state retention against the modeled contract. Live read/import compatibility and live mutation compatibility are recorded separately in the compatibility document.
