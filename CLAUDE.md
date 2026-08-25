# Opus Command — Unraid LXC Workspace

This workspace runs inside a persistent Unraid LXC container managed by Opus Command.

- Project files live in `/workspace` (bind-mounted from the Unraid project share).
- The container is persistent: tools you install (apt, `npm -g`, `pip`) survive
  stop/start and are not wiped — install normally.
- Files written under `/workspace` appear on the Unraid project share and persist.

## Docker inside the workspace

Docker works inside this LXC (overlayfs + cgroup v2). If you run it, the container
then has several IPv4 addresses: its LAN address on `eth0` (DHCP — it can change on
restart) plus Docker bridge gateways in the `172.16/12` block (`172.17.0.1`, …).
Only the `eth0`/LAN address is routable from the Unraid host and the LAN; the
`172.x` addresses are internal to this container. Find the current LAN IP with
`ip -4 route get 1.1.1.1` (the `src` field) and never hardcode it. Opus Command
reaches this workspace's terminal-agent at `<eth0-ip>:7681` and re-resolves that IP
on every connection, so a restart's new IP is handled automatically — but any
host-reachable service you expose must advertise the `eth0` address, not a `172.x`
bridge IP.

## Git and Opus Command

Opus Command's Git menu auto-discovers Git repositories under `/workspace`. It
checks `/workspace/.git` and one level down (e.g. `/workspace/<project>/.git`),
and recognises `.git` as a directory *or* a file — Git worktrees and submodules
store `.git` as a `gitdir:` pointer file. A repository nested in a subfolder
such as `/workspace/my-project` is fully supported; you do not need to move it
to `/workspace`.

- If more than one repository is found, the Git menu shows a repository picker
  and remembers the active one per project. Every Git-menu action (status, diff,
  commit, snapshot, push) operates on the active repository.
- Run Git commands from the repository root, or use `git -C <repo-root> ...`
  (for example `git -C /workspace/my-project ...`).
- If the project is not initialized and the user expects the Git menu to work,
  run `git init` in the repository folder before making changes.
- Check `git status --porcelain` before and after edits so the Git menu and your
  summary agree about changed files.
- Do not run destructive commands such as `git reset --hard`, `git clean -fd`,
  rebases, or history rewrites unless the user explicitly asks.
- Opus snapshots are annotated tags named `snapshot/YYYY-MM-DD-HH-MM-SS`; do not
  delete, move, or overwrite them unless the user explicitly asks.

Stage or commit only when the user asks. Otherwise leave changed files visible
for review in the Opus Command Git menu.


## Opus Managed Skills

Also read:
- .opus/skills/connectors.md
