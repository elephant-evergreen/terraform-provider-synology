# Repository instructions

- Write public documentation, examples, diagnostics, issues, pull requests, and commits in English.
- Use Go and HashiCorp's official Terraform Plugin Framework. Keep the development toolchain in mise.toml.
- Model DSM objects with typed resources. Do not expose generic API calls, shell execution, or a configuration document resource.
- Import, refresh, discovery, and planning must not change DSM configuration or user files. Authentication sessions are transient.
- Refresh every managed readable attribute. Fail on authentication, transport, permission, malformed-response, and unsupported-version errors; only confirmed absence removes an object from state.
- Never recreate an existing object to adopt it. Reject name collisions with an actionable import diagnostic.
- Keep data-bearing objects protected by default. Never delete, move, or rewrite user files as a side effect of configuration updates.
- Do not claim atomic rollback that DSM does not provide. Preserve identity after partial creation and expose uncertain outcomes for reconciliation.
- Keep resource passwords write-only, enforce verified HTTPS, reject redirects, and redact credentials, response bodies, and session tokens from logs and errors.
- Ordinary tests use synthetic local fixtures and the shipping provider. Live destructive tests require an explicitly authorized disposable environment.
- Never commit credentials, private identifiers, DSM exports, state, plan files, or personal data. Do not add code comments unless requested.
- Run bin/check-for-agent before publication. State exact DSM compatibility evidence and remaining limits.
- Preserve MPL-2.0 and third-party notices. AI-assisted commits require a reason and Co-Authored-By trailer.
