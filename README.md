# Atlas

Prototype web panel for managing a Linux server in Go (single binary).

## Features

- Dashboard: CPU / RAM / Disk / Network (via `/proc`, ~2s refresh).
- Files: directory listing, text preview, download, upload (restricted to a root directory).
- Files: create folders/files, rename, delete (recursive), edit text files.
- Terminal (not interactive yet): run a command and view the output.
- Processes: process list (top by RSS).

## Run

```bash
go run ./cmd/atlas -config ./atlas.json
```

## Install (recommended: GitHub Releases)

Atlas publishes ready-to-use artifacts in GitHub Releases:

- `AppImage` (x86_64)
- `.deb` (x86_64/arm64)
- `.rpm` (x86_64/arm64)
- `tar.gz` (x86_64/arm64)
- `install.sh` + `SHA256SUMS.txt`

Install the latest release:

```bash
curl -fsSL https://github.com/MrTeeett/Atlas/releases/latest/download/install.sh | bash
```

The installer can also initialize the config and create an admin user (it will offer random values for `port` / `base_path` / password and lets you override them):

```bash
curl -fsSL https://github.com/MrTeeett/Atlas/releases/latest/download/install.sh | bash -s -- --fresh
```

Install a specific version:

```bash
curl -fsSL https://github.com/MrTeeett/Atlas/releases/download/v0.0.1/install.sh | bash -s -- --version v0.0.1
```

Dev channel (the `dev` release tag, assets are replaced on every push to `main`):

```bash
curl -fsSL https://github.com/MrTeeett/Atlas/releases/download/dev/install.sh | bash -s -- --version dev
```

Force a specific install method:

```bash
# auto|appimage|deb|rpm|tar
curl -fsSL https://github.com/MrTeeett/Atlas/releases/latest/download/install.sh | bash -s -- --method tar
```

After installing via `.deb/.rpm`:

```bash
sudo systemctl enable --now atlas
```

Config and data locations (when installed via `.deb/.rpm`):

- config: `/etc/atlas/atlas.json`
- data/keys/users DB: `/var/lib/atlas/`

## Quick install (local, build from source)

The script builds the binary, generates a random `port`/`base_path`/`password` by default (and offers to enter your own if you decline), creates an `admin` user with full privileges, and prints the `/login` URL:

```bash
./deploy/install.sh
```

## One-command install (from GitHub, build from source)

Requirements: `bash`, `tar`, `go`, plus `curl` or `wget`.

```bash
curl -fsSL https://raw.githubusercontent.com/MrTeeett/Atlas/main/deploy/remote_install.sh | bash
```

You can pass arguments through to the installer, for example:

```bash
curl -fsSL https://raw.githubusercontent.com/MrTeeett/Atlas/main/deploy/remote_install.sh | bash -s -- --dir /opt/atlas --fresh
```

If you need to recreate the config/keys/users DB in the install directory:

```bash
./deploy/install.sh --dir /opt/atlas --fresh
```

Atlas prints the URL in logs on startup. You can also build the URL from `atlas.json`:
`http://<host from listen><base_path>/login`.

Config: `atlas.json` (JSON).

If `atlas.json` is missing, it is created automatically (default: everything is allowed, plus random `listen` and `base_path`).

The key and users DB are created next to the config:

- `atlas.master.key` — 32 bytes (base64), keep it with `0600` permissions
- `atlas.users.db` — encrypted users file

Create a login user (credentials are stored in the encrypted users DB):

```bash
go run ./cmd/atlas -config ./atlas.json user add -user admin -pass change-me
```

Permissions (examples):

```bash
# Regular user: view/files within process permissions
go run ./cmd/atlas -config ./atlas.json user set -user alice -exec=false -procs=false -fs-sudo=false

# Admin: access to Terminal, process management, and FS identity switching
go run ./cmd/atlas -config ./atlas.json user set -user admin -role=admin -exec=true -procs=true -fs-sudo=true -fs-any=true

# Process access + files as sysdba (no Terminal)
go run ./cmd/atlas -config ./atlas.json user set -user ops -exec=false -procs=true -fs-sudo=true -fs-users=sysdba
```

## Deploy to a remote server (recommended via SSH tunnel)

Build:

```bash
go build -trimpath -ldflags="-s -w" -o atlas ./cmd/atlas
```

Copy:

```bash
scp ./atlas user@server:/opt/atlas/atlas
scp ./atlas.json user@server:/opt/atlas/atlas.json
```

Tunnel:

```bash
ssh -L 8080:127.0.0.1:8080 user@server
```

## Security (important)

- By default it listens on `127.0.0.1:8080` — keep it that way and connect via an SSH tunnel or a reverse proxy with TLS.
- `enable_exec: true` enables executing shell commands on the server from the browser — this is dangerous. If you enable it, use TLS, strong credentials, restrict the root, and preferably run under a dedicated low-privilege user.
- Switching FS user in `Files` works via `sudo -n -u <user> atlas fs-helper ...` and requires a `sudoers` (NOPASSWD) rule for the Atlas binary; otherwise you'll get `403` instead of `500`.
  Example (service user `atlas`, binary `/opt/atlas/atlas`, allow only `sysdba`):
  - `/etc/sudoers.d/atlas`:
    - `atlas ALL=(sysdba) NOPASSWD: /opt/atlas/atlas fs-helper *`

## systemd

Example unit: `deploy/atlas.service` (adjust the path and `atlas.json` to your setup).

Create a user on the server:

```bash
/opt/atlas/atlas -config /opt/atlas/atlas.json user add -user admin -pass change-me
```
