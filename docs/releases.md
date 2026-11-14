# Releasing the Synology provider

Users find changes and upgrade instructions in [GitHub Releases](https://github.com/elephant-evergreen/terraform-provider-synology/releases). The release workflow verifies the tagged notes and signed packages before publishing to GitHub for [Terraform Registry](https://registry.terraform.io/providers/elephant-evergreen/synology).

## Decide when to release

Merge verified changes as they are ready. Publish when users need a new feature, a fix, or an important clarification. Release a compatible fix promptly when it blocks real use or addresses a security issue. Combine routine fixes, internal maintenance, and documentation improvements into the next useful release.

Before scheduling a breaking release, look for a compatible default, a deprecation period, or a state migration that preserves existing objects. Group related incompatible changes so users make one upgrade. Publish once the upgrade instructions and representative plans agree.

## Choose the version

| Change | Version while on 0.x |
| --- | --- |
| Existing configurations or state require migration, or a documented operation changes incompatibly | Next minor, such as `v0.4.0` → `v0.5.0` |
| Compatible feature, fix, documentation, or dependency update | When released, next patch, such as `v0.4.0` → `v0.4.1` |
| Optional release candidate for explicit testing | Target version plus `-rc.1`, then `-rc.2` for a revised candidate |

When a change simplifies an early interface, preserve NAS data, configuration, and resource identities through the documented upgrade. Treat incompatible changes to defaults, import IDs, deletion behavior, or state formats as upgrade changes. Once the provider reaches 1.0, use [Semantic Versioning](https://semver.org/).

## Write the release notes

Add `release-notes/<tag>.md` in English to the release PR. Write for someone deciding whether to upgrade.

- Start with **Breaking changes** when existing users must act or can encounter incompatible behavior. For each change, identify the resource or setting, affected versions and configurations, what changed, and the action to take before applying. Link to a versioned upgrade guide for multi-step changes. Include a pre-upgrade plan, what to back up, the expected post-upgrade plan, and how to recover. State if an older provider cannot read upgraded state.
- Start with **Upgrade notes** for other releases. State whether existing users need to change HCL or state. For a first release, tell readers how to adopt existing objects or settings.
- Use **Highlights** and **Fixes** where applicable. Name the affected resource or setting and explain the behavior the user will see. Link to the relevant PR for context. Use **Compatibility** when supported platforms or verification change, and distinguish fixture results from live verification.
- End with a GitHub comparison from the previous public stable tag to this tag. For the first release, link to the versioned provider reference instead. Use absolute URLs and pin links to supporting documentation to the tag so they work in GitHub Releases.

A compatible fix can be brief. For example, lead with `## Upgrade notes` and “Existing configurations and state need no changes.” Follow with the affected resource and the outcome under `## Fixes`, then the tag comparison.

## Publish and verify

1. Review the release notes with the changes they describe. For an incompatible release, check that the [upgrade verification](../CONTRIBUTING.md) covers the versions and migrations in the release notes. Reuse those results for the same candidate and repeat any checks affected by later changes.
2. After the PR is merged and its main-branch Check workflow passes, tag that exact commit. From a checkout with access to the repository, set `reviewed_commit` to that SHA and `release_tag` to the version matching the notes. Then run:

   ```sh
   git fetch origin main --tags &&
   git tag -a "$release_tag" "$reviewed_commit" -m "Release $release_tag" &&
   git push origin "refs/tags/$release_tag"
   ```

3. The **Release** workflow checks the tagged commit, signs the packages, and creates or resumes a GitHub draft. It compares the draft notes with the tagged file and checks each uploaded asset digest before publishing. A failed upload or mismatched digest can be retried on the same tag. If tagged files need correction, use a new version. Keep published tags and assets fixed. Put factual corrections in the GitHub release body under a dated correction.
4. The workflow waits for Terraform Registry and verifies a signed installation of the exact version. If ingestion times out, rerun the workflow. It leaves an already published release intact and checks the Registry again. A candidate requires an exact pin and may appear as the newest version on the Registry page.

## Registry setup

The public source is `elephant-evergreen/synology`. The release workflow builds Linux and macOS packages for amd64 and arm64, and publishes a protocol 6 manifest, SHA-256 checksums, and a detached GPG signature.

The [release public key](../keys/release.asc) is registered in the HCP Terraform public namespace with access to this repository through the Terraform Cloud GitHub App. Keep the private signing key and passphrase in the repository's `GPG_PRIVATE_KEY` and `PASSPHRASE` Actions secrets and back up the signing identity outside Git. See the [Terraform Registry publication guide](https://developer.hashicorp.com/terraform/registry/providers/publishing) when setting up or rotating the signing identity.
