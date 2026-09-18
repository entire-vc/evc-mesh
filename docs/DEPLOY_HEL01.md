# CI deploy to a private VM via a restricted SSH jump host

Production runs on a private network behind a single public gateway. GitHub-hosted CI
runners cannot reach the private addresses directly, so every deploy hops through a
**forwarding-only jump user** on the gateway host.

## Mechanism

- **Jump user** (`ghdeploy` in our setup) on the gateway: `nologin` shell, `authorized_keys`
  lines are `restrict,port-forwarding,permitopen="<vm_ip>:22" <product-deploy-pubkey>`.
  No shell/pty/exec — only `ssh -J` (`-W`) tunnels, and only to the one VM each key owns.
  A leaked repo secret cannot reach any other product's VM.
- **Target VM** root `authorized_keys` holds the same product deploy pubkey.
- The deploy key (`secrets.DEPLOY_SSH_KEY`) is authorized on **both** hops; `ssh-agent`
  (webfactory/ssh-agent) offers it to the jump and the target transparently.
- Each product gets its own private IP on the bridge and its own jump-user key —
  compromising one product's CI secret does not expose another's VM. The concrete
  address map is operational data, not published here.

## Workflow pattern

After `webfactory/ssh-agent`, add a step that writes `~/.ssh/config`. The gateway's public
IP and this VM's private IP come from CI secrets, never hardcoded in the workflow file
(`secrets.HEL01_JUMP_HOST`, `secrets.MESH_VM_HOST` in this repo — see `.github/workflows/deploy-backend.yml`):

```yaml
- name: Configure hel01 jump
  run: |
    mkdir -p ~/.ssh && chmod 700 ~/.ssh
    ssh-keyscan -H "${{ secrets.HEL01_JUMP_HOST }}" >> ~/.ssh/known_hosts 2>/dev/null || true
    {
      echo "Host <product>-vm"
      echo "  HostName ${{ secrets.MESH_VM_HOST }}"
      echo "  User root"
      echo "  ProxyJump hel01-jump"
      echo "  StrictHostKeyChecking accept-new"
      echo "Host hel01-jump"
      echo "  HostName ${{ secrets.HEL01_JUMP_HOST }}"
      echo "  User ghdeploy"
      echo "  StrictHostKeyChecking accept-new"
    } >> ~/.ssh/config
    chmod 600 ~/.ssh/config
```

Then target `<product>-vm` in every `ssh`/`rsync` (rsync reads `~/.ssh/config`, so ProxyJump
applies automatically). Never target an old public IP directly once a product has moved
behind the jump host — direct access is retired along with the migration.

## Registering a new product

1. On the gateway, append the product's deploy pubkey to the jump user's
   `authorized_keys` with `permitopen="<vm_ip>:22"`.
2. Inject the same pubkey into the VM root `authorized_keys`.
3. Set `secrets.DEPLOY_SSH_KEY` in the repo to the matching private key
   (fresh per-product key preferred — do not reuse across products).
4. Swap the workflow to the pattern above, adding `secrets.HEL01_JUMP_HOST` and a
   VM-specific host secret to the repo.
