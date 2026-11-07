# DSM compatibility

The initial release targets import and configuration readback on DSM 7.1.1, verified on a DS220j. This is not evidence for DSM 7.2 or 7.3, other models, domain accounts, encrypted shares or Btrfs-specific behavior.

| Resource | Verified live | Verified with synthetic HTTPS and Terraform |
| --- | --- | --- |
| shared_folder | List, complete managed readback, UUID, import and no-op plan | Create, update, protected delete, import, drift, replacement identity and interrupted creation |
| user | UID, metadata, expiration, group membership, import without password reset | Create, update, write-only password, omitted membership preservation, protected delete, import, drift, replacement identity and interrupted creation |
| group | GID, description, import and no-op plan | Create, update, protected delete, import, drift, replacement identity and interrupted creation |
| share_permissions | Complete explicit local-user/group policy, inheritance, UUID/UID/GID bindings, import and no-op plan | Full policy update, import, external drift, management-only destroy |
| smb, afp, nfs, ftp, sftp, time | Complete readback of each resource schema, import and no-op plan | Shared singleton implementation; SFTP create/import/update/drift/destroy, preservation of omitted settings |

Live verification imported two shared folders, three users, three groups, two permission policies and six service singletons into isolated local state. The reviewed import plan contained sixteen imports and zero creates, updates, replacements or deletions. The subsequent plan exited 0. Credentials were supplied through ephemeral SOPS values; the DSM password was absent from saved plan and state. Configuration metadata still enters state and needs protection.

No hardware mutation, deletion, interruption or data-restore acceptance test has been performed. Those paths are experimental until exercised on a dedicated disposable DSM environment. Fixtures model the observed read shape and researched mutation contracts; they cannot prove hardware writes. Do not use a production NAS as a destructive test environment.

API contract sources include the discovered DSM API catalogue, read-only observations on the stated version, [Synology's File Station API guide](https://global.download.synology.com/download/Document/Software/DeveloperGuide/Package/FileStation/All/enu/Synology_File_Station_API_Guide.pdf), [the DSM adapter at v0.8.1](https://github.com/batonogov/terraform-provider-synology-dsm/tree/v0.8.1/internal/client), and [N4S4's Core User adapter](https://github.com/N4S4/synology-api/blob/969216beecf08b25a1559046efb4c1f37fb8136c/synology_api/core_user.py). DSM Core administration APIs are not covered completely by a public stable specification. API presence alone is not lifecycle support.

## Additional policy coverage

The development extension also verified complete live readback for terminal access (SSH/Telnet flags), rsync service/account flags, Windows WS-Discovery and per-protocol transfer logging. Each additional resource has synthetic Terraform create/import/update/external-drift/management-only-destroy coverage, including changed configuration on Create and preservation of omitted flags. This does not establish real DSM mutation compatibility.

Shared-folder `hide_unauthorized_files` uses the `hide_unreadable` property requested through the vendor's `advance_setting` addition. Both observed shares returned true. Omitting this new attribute preserves the existing value; it does not change existing shares on upgrade. Explicit configuration owns and drift-detects it. Fixture tests cover explicit drift, omission during another metadata update, and local protection changes without an extra remote write.

These contracts were traced through the DSM 7.1.1 shipped UI sources `webman/modules/AdminCenter/admin_center.js` (SHA-256 `a78318e3afea8644606722eb20f647547d31a85884ef299e1c3b714ae66b1e5e`) and `webman/3rdparty/SMBService/smb-tab.js`. Source inspection was read-only; vendor JavaScript is not executed or redistributed by the provider. The UI explicitly sends individual changed logging/discovery flags, supporting preservation of omitted settings. Only the modeled flags are owned: SSH ports/ciphers/console policy, rsync credentials/custom configuration, detailed log filters/retention and log contents remain outside these resources.
