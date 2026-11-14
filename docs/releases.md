# Releasing the Synology provider

Users find changes and upgrade instructions in [GitHub Releases](https://github.com/elephant-evergreen/terraform-provider-synology/releases). The release workflow publishes the reviewed per-version Markdown file as the release body and attaches the signed packages for [Terraform Registry](https://registry.terraform.io/providers/elephant-evergreen/synology).

## Choose the version

| Change | Version while on 0.x |
| --- | --- |
| Existing configurations or state require migration, or a documented operation changes incompatibly | Next minor, such as `v0.4.0` → `v0.5.0` |
| Compatible feature, fix, documentation, or dependency update | Next patch, such as `v0.4.0` → `v0.4.1` |
| Optional release candidate for explicit testing | Target version plus `-rc.1`, then `-rc.2` for a revised candidate |

When a change simplifies an early interface, preserve NAS data, configuration, and resource identities through the documented upgrade. Treat incompatible changes to defaults, import IDs, deletion behavior, or state formats as upgrade changes. After 1.0, use [Semantic Versioning](https://semver.org/).

## Write the release notes

Add `release-notes/<tag>.md` in English to the release PR. Use the sections below when they apply.

- **Upgrade notes** first. Name the affected resources and versions, what changes for existing users, and what to do before applying. Link to a versioned upgrade guide if the steps require HCL or state migration. State whether users need to change configuration or state. For a migration, include what to back up, an example of the expected plan, and how to recover if the upgrade fails.
- **Highlights** and **Fixes**. Start each entry with the affected resource or setting. Describe what users will observe and link to the relevant PR. For a larger release, link to the GitHub comparison against the previous stable tag. Omit empty sections.
- **Compatibility**. Identify the NAS model and DSM version covered by live verification. Distinguish read and import from live writes and recovery. Link to the [DSM compatibility evidence](compatibility.md) at the release tag, using an absolute URL so it also works in GitHub Releases.

## Publish and verify

1. Review the release notes with the changes they describe. For an incompatible release, exercise the upgrade from the previous public version with representative existing HCL and state. Confirm that the documented procedure preserves existing objects and settings except for explicitly documented changes. Check that the plan contains only the intended actions before applying it in a test environment.
2. After the PR is merged and its main-branch Check workflow passes, tag that exact commit. From a checkout with access to the repository, set `reviewed_commit` to that SHA and `release_tag` to the version matching the notes. Then run:

   ```sh
   git fetch origin main --tags &&
   git tag -a "$release_tag" "$reviewed_commit" -m "Release $release_tag" &&
   git push origin "refs/tags/$release_tag"
   ```

3. Wait for Release to pass. Compare the GitHub Release body with the tagged Markdown file, check the signed assets, and confirm the tag appears in Terraform Registry with its versioned docs. In a fresh directory, pin that exact version and run `terraform init` to verify normal discovery and signature checks. For a release candidate, verify explicit installation and the GitHub prerelease marker.
4. If a run fails before publication, inspect the GitHub Release and Registry first. Retry the same tag after resolving a transient failure. If a partial draft blocks asset upload on retry, remove that unpublished draft and rerun the workflow. If the tagged files need a correction, merge it and use a new version. Once published, keep the tag and signed artifacts fixed. Publish factual corrections to the notes in the GitHub Release body with a dated correction.

## Registry setup

The public source is `elephant-evergreen/synology`. The release workflow builds Linux and macOS packages for amd64 and arm64, and publishes a protocol 6 manifest, SHA-256 checksums, and a detached GPG signature.

The [release public key](../keys/release.asc) is registered in the HCP Terraform public namespace with access to this repository through the Terraform Cloud GitHub App. Keep the private signing key and passphrase in the repository's `GPG_PRIVATE_KEY` and `PASSPHRASE` Actions secrets and back up the signing identity outside Git. See the [Terraform Registry publication guide](https://developer.hashicorp.com/terraform/registry/providers/publishing) when setting up or rotating the signing identity.
