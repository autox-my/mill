# Proxmox LXC Deployment Runbook (`CT 122` — `mill`)

Single-binary deployment of `mill` on Debian 13 LXC (`1c / 512M RAM / 8G disk`, < 50 MB RSS), exposed exclusively over Tailscale Serve (`https://mill.xantu-barb.ts.net`, serve-only, never funnel).

## 1. Build Linux Binary (from Mac)

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/mill-linux-amd64 ./cmd/mill
```

## 2. Provision CT 122 (on PVE as root)

```sh
# Copy deploy/proxmox/create-lxc.sh to PVE and run:
bash create-lxc.sh
pct exec 122 -- tailscale up --hostname=mill
```

## 3. Install & Start `mill.service`

Copy `build/mill-linux-amd64` and `deploy/proxmox/` to CT 122 (or `scp` to `/tmp/mill-deploy`), then inside CT 122:

```sh
bash /tmp/mill-deploy/install.sh /tmp/mill-deploy/mill-linux-amd64
```

Verify over Tailscale from your Mac:

```sh
TOKEN=$(ssh root@192.168.0.122 'grep ^MILL_API_TOKEN= /etc/mill/mill.env | cut -d= -f2')
curl -fsS -H "Authorization: Bearer $TOKEN" https://mill.xantu-barb.ts.net/stats
```

## 4. Connect Harnesses via `mill mcp` (Mac, OpenCode CT 120, Claude Code, Antigravity)

When `MILL_URL` and `MILL_API_TOKEN` are set, `mill mcp`, `mill slice`, `mill run`, `mill ingest`, `mill improve`, and `mill stats` transparently talk to CT 122 over HTTPS instead of a local SQLite file.

Install the local binary on each workstation/CT:

```sh
go build -o /usr/local/bin/mill ./cmd/mill
```

Configure MCP (`stdio`):

```json
{
  "mcpServers": {
    "mill": {
      "command": "/usr/local/bin/mill",
      "args": ["mcp"],
      "env": {
        "MILL_URL": "https://mill.xantu-barb.ts.net",
        "MILL_API_TOKEN": "<MILL_API_TOKEN>"
      }
    }
  }
}
```

## 5. Backup & Restore

- Weekly `vzdump 122` covers rootfs + `/var/lib/mill/mill.db`.
- Online SQLite snapshot inside CT 122:
  ```sh
  sqlite3 /var/lib/mill/mill.db ".backup '/tmp/mill-backup.db'"
  ```

## 6. CT 112 (`buzz`) Cutover & Decommission

Only after verifying at least 2 harnesses (e.g. Claude Code + Antigravity or OpenCode CT 120) against `https://mill.xantu-barb.ts.net`:

```sh
# On PVE as root: snapshot Buzz first, then stop CT 112 to reclaim 4c / 8G RAM
vzdump 112 --mode snapshot --compress zstd --storage tank-backup
pct stop 112
```
