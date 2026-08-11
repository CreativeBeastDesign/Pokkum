# Supply Chain Hardening v1

## Overview

1. Compile → bunexec adapter produces binary (SOURCE_DATE_EPOCH-pinned)
2. SBOM → syft adapter generates SPDX/CycloneDX from build output
3. Vuln gate → grype/trivy scans the SBOM, fails build above threshold
4. Package → packager adapter builds layer, appends to base image
5. Push → registry adapter pushes image, returns digest
6. Provenance → attestation adapter builds in-toto/SLSA predicate
7. Sign+attest → attestation adapter (cosign lib) signs image + attaches SBOM + provenance
8. Verify gate → admission controller (Kyverno/OPA) checks signature+provenance before deploy

## Trusted-builder mode for real SLSA L3

```
permissions:
  id-token: write
  contents: read

jobs:
  build:
    steps:
      - run: pokkum build --output=image-digest.txt
    outputs:
      digest: ${{ steps.build.outputs.digest }}

  provenance:
    needs: build
    steps:
      - uses: slsa-framework/slsa-github-generator@v2
        with:
          image: ${{ needs.build.outputs.digest }}
```
