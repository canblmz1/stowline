# Notices

Stowline is licensed under the Apache License 2.0 with the Commons Clause
License Condition v1.0 (see [LICENSE](LICENSE)).

Copyright 2026 The Stowline authors

## Third-party software

Stowline uses, and its installer redistributes, the following components
under their own licenses. Their license texts are available from the
projects' repositories.

**Shipped in the Windows installer**

| Component | License |
|---|---|
| [restic](https://github.com/restic/restic) 0.19.1 | BSD-2-Clause |
| [rclone](https://github.com/rclone/rclone) 1.75.0 | MIT |
| [Python](https://www.python.org) 3.12 embeddable distribution | PSF License |
| FastAPI, Starlette, Pydantic, Uvicorn, httpx, tzdata (setup wizard runtime) | MIT / BSD-3-Clause / Apache-2.0 |

**Compiled into the Go programs**

| Module | License |
|---|---|
| github.com/jchv/go-webview2, github.com/jchv/go-winloader | MIT |
| golang.org/x/sys, golang.org/x/exp | BSD-3-Clause |
| modernc.org/sqlite, modernc.org/libc, modernc.org/mathutil, modernc.org/memory | BSD-3-Clause |
| github.com/google/uuid, github.com/remyoudompheng/bigfft | BSD-3-Clause |
| github.com/dustin/go-humanize, github.com/mattn/go-isatty, github.com/ncruces/go-strftime | MIT |

**Server dependencies** (installed by pip): FastAPI (MIT), Uvicorn
(BSD-3-Clause), SQLAlchemy (MIT), Pydantic and pydantic-settings (MIT),
httpx (BSD-3-Clause), python-multipart (Apache-2.0), cryptography
(Apache-2.0 or BSD-3-Clause), psycopg (LGPL-3.0, optional, for PostgreSQL).

**Fonts bundled with the admin panel**: Inter and JetBrains Mono (SIL Open
Font License 1.1), Material Symbols (Apache-2.0).

**Deployment images**: PostgreSQL (PostgreSQL License), Caddy (Apache-2.0).
