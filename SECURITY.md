# Security

Stowline is maintained in spare time. If you find a security problem,
please use GitHub's private vulnerability reporting (Security → Report a
vulnerability) or open an issue **without exploit details** and ask for a
private contact.

Before running it for real data, read the security model in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and the deployment notes in
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md). In particular:

- expose only the reverse proxy; never the control plane or gateway ports;
- set your own `STOWLINE_SECRET_KEY`, admin password and gateway token;
- never enable `STOWLINE_LAB_MODE` or `STOWLINE_ALLOW_INSECURE_HTTP` on a
  server that holds real data;
- keep `STOWLINE_ESCROW_KEY` and each computer's backup key somewhere safe:
  without the key a restic repository cannot be opened by anyone.
