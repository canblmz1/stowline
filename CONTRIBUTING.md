# Contributing

Thanks for helping! Stowline is maintained in spare time, so replies can take a while.

- **Bugs**: open an issue with what you did, what happened and what you expected, the Stowline version (panel → Settings → Version) and, if relevant, the agent's Windows version. Remove passwords, keys, tokens and personal paths from logs.
- **Security problems**: do not open a public issue with details — see [SECURITY.md](SECURITY.md).
- **Pull requests**: keep them focused, add or update tests, and run
  `go test ./...` and `pytest server/tests gateway/tests tools/stowline-setup/tests`.
  If you change interface text, add the Turkish → English line to
  `i18n/tr-en.json` and run `python scripts/build-i18n.py` (CI checks it).

By contributing you agree that your contribution is licensed under the
project's license (Apache 2.0 with the Commons Clause, see [LICENSE](LICENSE)).
