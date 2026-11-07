# Releases

The intended Registry source is elephant-evergreen/synology. Publish only a reviewed default-branch commit with successful checks and evidence matching its documented DSM support.

The release workflow builds protocol-6 packages for Linux and macOS on amd64 and arm64, creates SHA-256 checksums and a detached GPG signature, and uploads the Registry manifest. Signing credentials are available only to the tag-triggered release job, never ordinary pull requests.

Register the public signing key and repository in the existing elephant-evergreen HCP Terraform public namespace. Do not expose private key material or request broader repository access than required. Confirm Registry ingestion and a fresh, normal terraform init with the pinned version and verified signature before calling a release available.

The namespace signing identity is shared with the GL.iNet provider. Its public key is [keys/release.asc](../keys/release.asc), fingerprint `9699ECB55B3E8B042C68A4B063527B92B224D3DD`. The encrypted private key and passphrase are provided only through the tag workflow's GPG_PRIVATE_KEY and PASSPHRASE repository secrets.

Keep the signing identity backed up outside Git. A GitHub release alone does not demonstrate Registry installation or NAS compatibility.
