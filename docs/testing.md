# Verification boundaries

| Evidence | Establishes |
| --- | --- |
| HTTP/TLS fixtures | Certificate rejection, redirect rejection, bounded responses, error redaction and no blind mutation retries |
| Terraform protocol tests against local HTTPS fixtures | Schema, lifecycle, import, readback, external drift and stable plans against the fixture contract |
| Read-only inspection of a named DSM version | Only the API capabilities, response shapes and import/read behavior actually observed on that version |
| Dedicated disposable DSM acceptance environment | Only the CRUD, interruption, permission and recovery scenarios executed there |

Live compatibility is not inferred from API names alone. Preserve authenticated read failures as errors. A zero plan cannot prove drift detection unless out-of-band changes have also been tested in the corresponding contract.

Migration of a personal NAS must use import-only plans with no remote configuration changes, fixed provider packages, protected external state and baseline file-sharing checks. Any later mutation requires an adequate data backup and a separate review of its actual effects. A DSM configuration export and Terraform state do not back up shared-folder data.

Storage reconstruction, firmware updates, file contents and backup execution are outside this provider's configuration lifecycle. Cross-resource atomic rollback is not guaranteed by DSM.

## Write-acceptance environment

Hardware write evidence requires a dedicated DSM instance that holds no user data. The read-only target used for the observed DSM 7.1.1 behavior cannot serve this purpose: it is an ARM model without Virtual Machine Manager support and it is a production NAS. Destructive tests must not run on it.

A write-acceptance host should meet:

- x86-64 Synology model supported by DSM 7.2 or later, with enough memory for a dedicated test volume.
- A dedicated test volume and account, no production folders, and no QuickConnect or WAN exposure.
- A fresh configuration snapshot before each run and a documented restore path.

The matrix, per resource:

1. Import an existing object, confirm an import-only plan and a no-op plan.
2. Create, read back every managed attribute, and confirm a no-op plan.
3. Update each writable attribute, read back, and confirm a no-op plan.
4. Change an attribute out of band, confirm drift, and plan the correction.
5. Delete with protection disabled, confirm absence, and confirm the destroy check passes.
6. Inject a lost response, a 5xx, and a connection drop during a write; confirm the resource does not replay an ambiguous change and reports the state it can prove.
7. Repeat the create, update, and delete sequence twice to catch replacement-identity regressions.

Acceptance evidence stays in a private note. Public documentation records only the DSM version, model, and whether each operation is hardware-write verified. A resource that has not run this matrix remains marked experimental.

