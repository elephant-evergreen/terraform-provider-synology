# Contributing

Use the pinned tools from mise.toml and run bin/check-for-agent. Keep public documentation and diagnostics in English, examples synthetic, and all credentials and device exports outside Git.

New resource support requires import without mutation, complete readback of managed readable fields, stable no-op plans, external-change detection, confirmed disappearance, protected deletion, and failure-path tests. A named-object resource must never acquire ownership of a pre-existing object during Create. Service singletons and complete permission policies are configuration resources: Create applies their declared settings, Import adopts unchanged, and Delete only releases management. Preserve state identity after partial creation and do not blindly retry ambiguous writes.

An asynchronous singleton may require import before any write when replacement could otherwise discard an interrupted operation baseline. Such resources must refuse stateless Create and test import-block adoption, Update, task discovery, read-only resumption, uncertain acknowledgement, replacement protection and management release.

Use only verified DSM API contracts. Fixture tests establish Terraform behavior against the fixture contract; they do not establish compatibility with a real DSM version. Live destructive acceptance tests require an explicitly authorized disposable environment. Do not use a personal NAS for deletion, lockout or restore experiments.

Add generated Registry documentation and synthetic examples with each public schema. Reconcile changes with the stated management boundary, including the distinction between an explicit deny and absence of a grant. Preserve MPL-2.0 and upstream notices. Include the reason for changes and an AI co-author trailer when applicable.

After dependency changes, run `bin/generate-licenses` and commit the resulting THIRD_PARTY_LICENSES.txt. CI checks the pinned Go vulnerability database tool and regenerates the license report used in release archives.
