# CI deploy to hel01 private VMs (via `ghdeploy` jump)

All products migrated behind **hel01** (`66.151.34.194`, Proxmox) run on the private
bridge `10.10.10.0/24` behind a single public IP. GitHub-hosted CI runners cannot reach
`10.10.10.X` directly, so every deploy hops through a **forwarding-only jump user** on hel01.

## Mechanism

- **Jump user** `ghdeploy` on hel01: `nologin` shell, `authorized_keys` lines are
  `restrict,port-forwarding,permitopen="<vm_ip>:22" <product-deploy-pubkey>`.
  No shell/pty/exec — only `ssh -J` (`-W`) tunnels, and only to the one VM each key owns.
  A leaked repo secret cannot reach any other product's VM.
- **Target VM** root `authorized_keys` holds the same product deploy pubkey.
- The deploy key (`secrets.DEPLOY_SSH_KEY`) is authorized on **both** hops; `ssh-agent`
  (webfactory/ssh-agent) offers it to the jump and the target transparently.

## VM map (VMID ≠ IP octet)

| Product | VM IP | Product | VM IP |
|---|---|---|---|
| mesh | 10.10.10.10 | mcp-gate | 10.10.10.50 |
| billing | 10.10.10.20 | argus | 10.10.10.60 |
| casdoor | 10.10.10.25 | contenthub | 10.10.10.70 |
| spark | 10.10.10.30 | tgbot | 10.10.10.80 |
| team-relay | 10.10.10.40 | listmonk | 10.10.10.90 |
| | | sites | 10.10.10.100 |

## Workflow pattern

After `webfactory/ssh-agent`, add a step that writes `~/.ssh/config`:

```yaml
- name: Configure hel01 jump
  run: |
    mkdir -p ~/.ssh && chmod 700 ~/.ssh
    ssh-keyscan -H 66.151.34.194 >> ~/.ssh/known_hosts 2>/dev/null || true
    {
      echo "Host <product>-vm"
      echo "  HostName 10.10.10.X"
      echo "  User root"
      echo "  ProxyJump hel01-jump"
      echo "  StrictHostKeyChecking accept-new"
      echo "Host hel01-jump"
      echo "  HostName 66.151.34.194"
      echo "  User ghdeploy"
      echo "  StrictHostKeyChecking accept-new"
    } >> ~/.ssh/config
    chmod 600 ~/.ssh/config
```

Then target `<product>-vm` in every `ssh`/`rsync` (rsync reads `~/.ssh/config`, so ProxyJump
applies automatically). Never target the old public IPs — they are dead.

## Registering a new product

1. On hel01, append the product's deploy pubkey to `/home/ghdeploy/.ssh/authorized_keys`
   with `permitopen="<vm_ip>:22"`.
2. Inject the same pubkey into the VM root `authorized_keys`
   (`qm guest exec <vmid> -- ...`).
3. Set `secrets.DEPLOY_SSH_KEY` in the repo to the matching private key
   (fresh per-product key preferred — do not reuse across products).
4. Swap the workflow to the pattern above.

Setup script: `bob/scripts/hel01-ghdeploy-register.sh` (idempotent).
