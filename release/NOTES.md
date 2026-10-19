Release artifacts now include the exact Windows agent and its generated
manifest. Server images consume the same bundle; agent version, installer pin
and update pin refer to the same build.

New release builds remain unqualified for automatic fleet updates. Selected
canary devices can test a candidate before operator-reviewed real Windows
qualification. See [the update procedure](https://github.com/canblmz1/stowline/blob/main/docs/RELEASE-UPDATES.md).
The public 0.1.1 agent needs the documented one-time manual binary update before
it can accept an unqualified canary through the new separate permission field.

Agent updates preserve enrollment and existing recovery material. Backup and
restore admission share the update restart gate. The fresh installer preserves
and stops at any existing installation; it no longer offers teardown as an
update. Direct wizard repair refuses to re-enroll an unverified identity. The
self-check and failed-boot rollback tests use synthetic fixtures; they do not
constitute real Windows service, VSS or device qualification.
