## Summary

<!-- Brief description of what this PR changes and why. -->

## Type of change

- [ ] Bug fix (non-breaking change which fixes an issue)
- [ ] New feature (non-breaking change which adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Refactor (no behavior change, code cleanup)
- [ ] Documentation update
- [ ] CI/CD change

## Checklist

- [ ] My code follows the [CONTRIBUTING.md](../CONTRIBUTING.md) guidelines
- [ ] I have run `make lint` and `make test` locally — both pass
- [ ] I have run `make check-version` — no version drift
- [ ] I have added tests that prove my fix is effective or my feature works
- [ ] I have updated the README / docs if needed
- [ ] My changes generate no new golangci-lint warnings
- [ ] I have not added any new direct dependencies without discussion (we keep the dep diet tight — currently 10 direct deps)

## Test plan

<!-- How did you verify this works? -->

- [ ] Unit tests pass (`make test`)
- [ ] Lint passes (`make lint`)
- [ ] Build succeeds (`make build`)
- [ ] Helm chart lints (`make helm-lint`)
- [ ] Manually tested end-to-end (describe below)

## Additional notes

<!-- Anything else reviewers should know. -->
