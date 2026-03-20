---
description: Create a release (e.g., v0.1.0)
allowed-tools: Bash, Read, Edit, Glob, Grep
---

Create a release of oinc. The argument is the version tag (e.g., v0.1.0).

1. Verify clean working tree:
   - Run: git status
   - If there are uncommitted changes, stop and ask the user to commit or stash first
2. Ensure we're on main and up to date:
   - Run: git checkout main
   - Run: git pull origin main
3. Run tests:
   - Run: go test ./...
   - If tests fail, stop
4. Build to verify:
   - Run: make build
   - If build fails, stop
5. Tag the release:
   - Run: git tag $ARGUMENTS
6. Ask the user to push the tag:
   - Tell them to run: git push origin main --tags
   - The release workflow (.github/workflows/release.yml) will build cross-platform binaries and create the GitHub release automatically
7. After user confirms they've pushed, verify:
   - Run: gh release view $ARGUMENTS (may take a minute for CI to complete)

Important notes:
- This repo uses a single remote (origin = jasonmadigan/oinc)
- The release workflow triggers on v* tags and handles binary builds + GitHub release creation
- Do NOT push -- ask the user to do it
- Version tag must start with "v" (e.g., v0.1.0, v1.0.0)
- Cross-platform binaries (darwin/linux, amd64/arm64) are built by CI, not locally
