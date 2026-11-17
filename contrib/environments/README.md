# Codex execution environments

The default registry is `~/.config/cc-connect/environments/*/environment.toml`.
An agent can override it with `environment_dir` in its options. Only enabled,
explicitly registered environments are scanned. `local` always means the original
project agent. State and selected UUIDs are stored separately per environment.

```toml
name = "emodia"
enabled = true
kind = "docker"
container = "emodia"
work_dir = "/project"
connect_script = "./connect.sh"
start_script = "./start.sh"
```

Script paths are relative to the entry file, or absolute. Connect scripts must
use raw stdin/stdout and send diagnostics to stderr. They must not allocate a
TTY or await input. A typical script ends with `exec docker exec -i ... codex
app-server proxy`. Set the correct UID/GID, HOME/CODEX_HOME, Node PATH and proxy
variables; copy these settings from your normal container entry script. Reuse
the existing shared server instead of starting another writer. Startup scripts
are explicit, noninteractive and limited to 30 seconds. The bot never runs an
interactive `docker exec -it ... bash` as a protocol connection.

Chat commands (administrator only):

- `/docker`: list configured environments.
- `/docker emodia`: select the container for this chat.
- `/list`: list remote Codex sessions through its shared server.
- `/switch UUID`: resume that environment's UUID.
- `/docker local`: restore the original local environment and its selection.
- `/docker start emodia`: run its configured startup script.

`python3.11 contrib/environments/manage.py list` checks container runtime status.
Its `connect NAME` and `start NAME` actions also support argument arrays via
`connect_args`/`start_args`. The bot connector currently uses script paths alone;
put arguments inside your administrator-owned wrapper script.

Current limits: no cross-environment `/list all`, no automatic UUID search
across environments. Quoted replies resolve the trusted bot footer environment and UUID without changing the current selection. Legacy replies without an environment footer are resolved across the whitelist; ambiguous UUIDs fail explicitly.
Remote backend transcript import for `/history` is not implemented: the bridge
retains messages it handled, but never reads local Codex history as remote
history. Existing containers need their own logged-in Codex/shared server.

Tests: `python3.11 -m unittest discover -s contrib/environments -v`.

Host helper commands `/shell`, `!`, `/show`, `/diff`, `/dir` and shell scheduling/custom-command management are blocked while a remote environment is selected; ask the container's Codex to perform the operation instead. They must not silently execute on the host. Switch to `local` to use host helpers.

Final replies include the immutable execution origin before the UUID footer:
`Environment: Local` or `Environment: emodia` (localized). Changing the selected
environment while a task runs does not relabel that task's result. External
reply collectors may set `environment` in their private sync configuration;
the default is `local`.

## Preserve the terminal directory with a shared server

A Bash wrapper that adds `codex --remote` must also forward the terminal's
working directory. With Codex 0.167.0, an omitted `--cd` sends `cwd="."` during
remote configuration loading. The server resolves that path from its own working
directory, so a server started in the home directory can open projects there
even when the terminal is in a different directory.

Run this helper where the terminal and app server share the same filesystem paths:

```bash
python3 contrib/environments/configure_shell.py \
  --rcfile "$HOME/.bashrc" \
  --codex-home "$HOME/.codex" \
  --server-url "unix://$HOME/.codex/app-server.sock" \
  --name emodia
source "$HOME/.bashrc"
```

Use the existing server endpoint and the startup file actually loaded by the
container shell. The helper defaults to `--cd "$PWD"` at invocation time and
preserves explicit `-C`/`--cd` arguments. It upgrades the legacy three-line
`# CC-Connect NAME shared server` block, preserves surrounding settings and the
initial backup, and is safe to rerun. Include the same command in deployment
scripts so existing installations receive updates. Reload the startup file or
open a new shell before starting a new Codex session.

## Automatic container setup

Unified deployment enables `cc-connect-environments-PROJECT.service`. Every ten
seconds it checks only enabled registry entries. A detected running container
with Codex is provisioned on registration, configuration/source change, or
container restart. Set `auto_configure = false` to opt out. `user`, `codex_home`,
`codex_binary` and `sync_session` override detection/target defaults. Existing
custom connect/start scripts remain administrator-controlled.

Provisioning merges and backs up `hooks.json`, installs Stop and PermissionRequest
handlers under the container's Codex home, and enables a dedicated host-side
`cc-connect-relay-PROJECT-ENVIRONMENT.service`. Private request files travel
through `docker exec` stdio; no TCP listener, mounted host API socket or copied
Feishu credentials are required. Disabling/removing an entry stops its relay.
The relay forces the configured project/chat/environment and permits only reply,
and approval endpoints. Bridge-owned final replies are skipped
using the originating turn marker.

Installation does not bypass Codex hook trust: review/load the new hooks in the
container's Codex client. Codex login and a compatible shared server must already
be available. A successful installation or relay connectivity check does not prove that
Stop/PermissionRequest hooks are trusted or that a real Feishu approval completed.
