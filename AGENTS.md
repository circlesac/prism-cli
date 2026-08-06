# Repository guidance

This is a public repository. Keep README files, docs, CLI help, package
descriptions, release notes, and pull request text focused on commands that
users need to install, sign in, add or remove provider accounts, configure a
client, and verify that it works.

- Do not document private Prism or Vault implementation details, credential
  storage or encryption modes, internal authentication chains, development
  endpoints, unfinished organization flows, admin APIs, or user-specific
  exceptions unless the user explicitly asks for that public documentation.
- Use `example.com` identities and generic account names in documentation and
  tests. Do not add real personal or company email addresses, account IDs,
  tokens, or credentials.
- Before committing or pushing, review the complete diff and search the changed
  files for internal details and real identities. If the user asks to review a
  diff first, do not commit or push until they have reviewed it.
- Documentation cleanup must not change supported runtime behavior unless the
  user explicitly requests a behavior change.
