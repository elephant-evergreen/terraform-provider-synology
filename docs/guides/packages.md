---
page_title: "Package adoption and policy"
subcategory: ""
description: "Adopt installed packages and manage running state and update policy."
---

# Package adoption and policy

## Adopt an installed package

Read `data.synology_packages` to discover installed IDs, versions, placement and lifecycle flags. Import each existing package by its ID; import never reinstalls it. `ExamplePackage` in examples is synthetic. Declare the observed running policy with deletion protection enabled in an import-only plan. Version and volume are read-only observations, so changes by Package Center remain visible without triggering a downgrade or relocation.

This resource never installs, upgrades, repairs or relocates software. The verified DSM quick-install API selects by package ID and channel, without binding its mutation to an exact preflight version. Checking a version before that request cannot prevent a catalogue race, and detecting a different version after installation cannot undo application-data migrations. An exact-artifact installation contract, signature validation and failure recovery must be established before those operations can be exposed. Use DSM Package Center for installation and import the resulting package. No generic API, shell or custom SPK workflow is provided.

## Start or stop a package

Start/stop operations require DSM lifecycle eligibility and a queue containing only the requested package. Unknown or transient statuses are not reported as stopped. Package operations are serialized through final inventory readback; a successful HTTP acknowledgement alone does not complete the operation. Acknowledged operations retained in Terraform private state resume through read-only target convergence. Unacknowledged updates require DSM inspection and explicit management release/import, rather than adopting a same-ID installation automatically. Back up state, inspect Package Center and application data, release management with a removed block using `destroy = false`, and import the confirmed installation. A process exit or lost Terraform RPC response can lose pending metadata before state is saved; inspect DSM after an interrupted apply.

## Uninstall a package

Uninstall is protected by default. Disable protection in a separate reviewed apply only after backing up application data. Built-ins, custom uninstall wizards and extra dependency actions remain refused. An uncertain uninstall is not replayed; refresh can confirm package absence. Management release never uninstalls software. Package configuration and state do not back up application data, and no rollback of package migrations is promised.

## Set Package Center update policy

`synology_package_center` owns global automatic-update mode, selected package sets, beta-channel selection and notification flags. Its setter preserves omitted coupled settings and translates DSM's boolean channel observation to the vendor's `stable`/`beta` request. Configure selected sets only in enabled custom mode, include installed IDs and keep all-update/important-update selections disjoint. Trust level and mail readiness are observed only; no signature override, SMTP setup, default-volume assumption or interactive policy is inferred. Automatic-update policy does not wait for future scheduled upgrades and does not pin exact package versions.

## Verification scope

Contracts are traced through the shipped DSM 7.1.1 Package Center UI (`webman/modules/PkgManApp/PkgManApp.js`, SHA-256 `f539b43ff69c9c956b7af1ec6024e7d9e4206b46281a557c8d390f5e4bda0102`). Installed reads and policy observations are verified separately from fixture lifecycle evidence. The live online-catalogue read timed out in the inspected environment; no hardware installation or deletion was attempted.
