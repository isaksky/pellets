# Frozen release fixtures

Keep these databases immutable. Tests copy each fixture before opening it; do not
regenerate old fixtures from the current migration files.

| File | Producer | Schema |
| --- | --- | --- |
| `released-v1.db` | Original released fixture | 1 |
| `released-v24.db` | Published Pellets v0.4.0 macOS ARM64 archive | 24 |
| `released-v26.db` | Pellets v0.5.0 macOS ARM64 release archive | 26 |

The schema-24 fixture was created in a disposable independent Git repository with
the published v0.4.0 executable. Its archive SHA-256 is
`0fa5bc96b8ee0b919c6a44ca6583ab608e6c7af125256d8da7f2b6443fa82885`, checked
against the published checksum manifest. It contains one open pellet with a
Unicode Markdown description, group and external ID, plus one agent-created
memory. SQLite's backup API captured the closed-command database.

The schema-26 fixture is that database after opening it with the v0.5.0 executable
and setting the pellet's model to `custom-model` and reasoning effort to `high`.
The release archive SHA-256 is
`e7b5617894cfab03145cf2d61e8717be81c489726e910d1a768ed2815350612f`.
Its repository paths refer to the discarded temporary fixture, never a user's
project. Storage tests open copies directly and do not discover those paths.

`TestRecentReleasedDatabaseFixtures` checks forward opening, authoritative text,
both search indexes, and preservation/defaulting of execution preferences.
