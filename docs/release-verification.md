# Release Verification Guide

cascade releases are signed using two cryptographic mechanisms and include SLSA build provenance to ensure authenticity and integrity.

## Verifying cosign signatures (recommended)

Cascade uses keyless cosign signing via Sigstore, which does not require key management. To verify a release:

1. Install cosign (https://github.com/sigstore/cosign/releases).

2. Download the release artifacts and signatures from the GitHub release page (checksums.txt, checksums.txt.sig, checksums.txt.pem, and the archives).

3. Verify the checksums file signature:

```bash
cosign verify-blob \
  --certificate=checksums.txt.pem \
  --signature=checksums.txt.sig \
  --certificate-identity-regexp='^https://github.com/stablekernel/cascade' \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  checksums.txt
```

The certificate is issued by Sigstore's public certificate authority. cosign automatically verifies the certificate chain and confirms the signature was created by GitHub Actions during the release workflow run.

4. Verify the checksums match the downloaded binaries:

```bash
sha256sum -c checksums.txt
```

## Verifying GPG signatures

For users who prefer traditional GPG verification:

1. Obtain the cascade maintainer's public key from `docs/cascade-release-public-key.asc` in this repository and import it:

```bash
gpg --import docs/cascade-release-public-key.asc
```

2. Download the release artifacts and `.asc` signature files from the GitHub release page.

3. Verify the checksums file signature:

```bash
gpg --verify checksums.txt.asc checksums.txt
```

4. If verification succeeds, verify the checksums match the downloaded binaries:

```bash
sha256sum -c checksums.txt
```

## Verifying SLSA provenance

Cascade releases include SLSA build provenance that provides cryptographic evidence about how the artifacts were built. The provenance is stored in GitHub's attestation store and verified with the GitHub CLI. To verify:

1. Install the GitHub CLI (https://cli.github.com) if not already installed.

2. Verify the provenance for a release artifact:

```bash
gh attestation verify cascade_VERSION_linux_amd64.tar.gz \
  --repo stablekernel/cascade \
  --certificate-identity https://github.com/stablekernel/cascade/.github/workflows/release.yaml@refs/tags/vVERSION
```

This verifies that the artifact was built by the release workflow for the specified tag and that the provenance is signed by GitHub.

## Reproducing the build

Cascade builds are designed to be bit-for-bit reproducible using GoReleaser. To reproduce:

1. Check out the specific release tag:

```bash
git clone https://github.com/stablekernel/cascade.git
cd cascade
git checkout v0.X.Y
```

2. Ensure Go 1.25 is installed (the version used for official releases).

3. Build with the same flags used in the release workflow:

```bash
goreleaser build --single-target --clean --skip-post-hooks \
  --id cascade
```

4. Compare the output with the official release binary:

```bash
sha256sum dist/cascade_linux_amd64/cascade
```

If the checksum matches the official checksums.txt, the build is reproducible.

## Trust model

cosign keyless signing uses an OIDC token issued by GitHub Actions during the release workflow. The token proves the signature was created during a specific GitHub Actions run in the cascade repository on the specified tag. Verification automatically confirms:

- The signature was created by the GitHub Actions runner (not a local machine).
- It was created during a release workflow run in the cascade repository.
- It is bound to the release workflow ref (the release tag) recorded in the certificate.

This model provides strong authenticity guarantees without requiring separate key distribution or management.
