---
page_title: "QuickConnect policy"
subcategory: ""
description: "Adopt QuickConnect policy without account registration or automatic router changes."
---

# QuickConnect policy

Import `synology_quickconnect` with ID `quickconnect`. Match the observed enabled flag, identifier, relay flag and complete service-permission map. All service IDs returned by DSM must be represented when declaring the map; unknown or omitted IDs are refused. Automatic router port forwarding is owned separately by `synology_quickconnect_upnp` and never enabled implicitly.

The provider uses the existing Synology account binding. Account registration, browser login, terms and real-name consent remain explicit DSM workflows. Changing the identifier requires enabled QuickConnect. Enabling occurs after other requested settings; disabling occurs first. These steps are not atomic and no rollback is promised. A configured enabled flag is not evidence of WAN connectivity, DNS resolution or successful relay access. See [Synology QuickConnect documentation](https://kb.synology.com/en-me/DSM/help/DSM/AdminCenter/connection_quickconnect).

Partial or unacknowledged writes retain a reconciliation guard in state. Read observes actual policy but cannot silently authorize a retry or erase the guard. Inspect DSM, back up state, release management with a removed block using `destroy = false` and import the confirmed policy before another change. Destroy does not unregister an identifier or disable a service. Process termination before Terraform saves the RPC response can lose this baseline; inspect DSM after any interrupted apply. Configuration, identifiers and account metadata enter state; credentials and DSM sessions do not.
