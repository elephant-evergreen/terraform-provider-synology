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
