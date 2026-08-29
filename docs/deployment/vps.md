# Installing a Custom (Non-Official) Plugin onto a Remote VPS

This guide documents the procedure for deploying custom or unofficial plugins (such as `antigravity-cloak`) to a production CLIProxyAPI instance running on a remote VM (e.g. GCP VM reached through the reverse-proxied management API at `https://vps.monet.uno/api-cli`).

All operations must be performed via the management API (`/v0/management`) using the Bearer management key in the `Authorization: Bearer <key>` header. Direct SSH access or host file edits are not required.

## Key Operational Facts
- The official store registry is always loaded. Your own plugin is only visible after its registry is added as an extra store-source. If you skip this, install returns 404 (host does not know the plugin id).
- The VPS config is NOT guaranteed to match the local `config.yaml`. The running VPS box may be missing both the custom store-source and the plugin config block even if local has them. Always read the live VPS config first.
- The install / enable / configure calls (`plugin-store/<id>/install`, `plugins/<id>/enabled`, `plugins/<id>/config`) persist their own `plugins.configs.antigravity-cloak` block back into the remote config. You only have to hand-add the `store-sources` lines in step 2 — do NOT hand-edit or re-upload the plugin config block, or you will fight the host.
- There is NO narrow endpoint for store-sources (`/v0/management/plugin-store/sources`, `/plugin-store-sources`, `/store-sources` all return 404). The only way to add a store-source is to edit the full config via `GET`/`PUT /v0/management/config.yaml`.
- `config.yaml` GET returns raw YAML bytes. In PowerShell, read with `Invoke-WebRequest` and decode `.Content` as UTF-8 (it returns as `byte[]`, not `string`).

## Deployment Procedure (PowerShell)

Set `$base` and `$key` to your VPS API endpoint and management key:

1. **Back up the live config first**:
   ```powershell
   # GET /v0/management/config.yaml -> save bytes verbatim (LF, no CRLF)
   $resp = Invoke-WebRequest -Uri "$base/v0/management/config.yaml" -Headers @{ Authorization = "Bearer $key" }
   [System.IO.File]::WriteAllBytes("config.backup.yaml", $resp.Content)
   ```

   *Keep this backup. It is your clean rollback point to restore the pre-deployment state with `PUT /v0/management/config.yaml`. Note: if you restore this initial backup after a failed deployment, re-apply the store-source configuration (Steps 2–3) before attempting to reinstall the plugin.*

2. **Add store-source**:
   Build the new config by inserting ONLY the `store-sources` lines under `plugins:` (right after `enabled: true`, before `configs:`). Diff against the backup to confirm no other lines are modified.
   ```yaml
   plugins:
     enabled: true
     store-sources:
       - https://raw.githubusercontent.com/monet88/antigravity-cloak/main/registry.json
     configs:
       ...
   ```

3. **Upload updated config**:
   ```powershell
   # PUT /v0/management/config.yaml with the updated YAML
   Invoke-RestMethod -Uri "$base/v0/management/config.yaml" -Method Put -Headers @{ Authorization = "Bearer $key"; "Content-Type" = "text/yaml" } -Body $yamlContent
   # Expect {"ok":true,"changed":["config"]}. Config reloads live, no container restart needed.
   ```

4. **Verify store availability**:
   ```powershell
   Invoke-RestMethod -Uri "$base/v0/management/plugin-store" -Headers @{ Authorization = "Bearer $key" }
   # Confirm the new store source and the antigravity-cloak entry appear with installed: false.
   ```

5. **Install plugin binary**:
   ```powershell
   # POST /v0/management/plugin-store/antigravity-cloak/install
   Invoke-RestMethod -Uri "$base/v0/management/plugin-store/antigravity-cloak/install" -Method Post -Headers @{ Authorization = "Bearer $key"; "Content-Type" = "application/json" } -Body '{"version":"0.4.0"}'
   # Host downloads matching GOOS/GOARCH .so from GitHub release into plugins/linux/amd64/antigravity-cloak-v<ver>.so.
   # Expect restart_required: false.
   ```

6. **Enable plugin**:
   ```powershell
   Invoke-RestMethod -Uri "$base/v0/management/plugins/antigravity-cloak/enabled" -Method Patch -Headers @{ Authorization = "Bearer $key"; "Content-Type" = "application/json" } -Body '{"enabled":true}'
   ```

7. **Configure plugin parameters**:
   ```powershell
   Invoke-RestMethod -Uri "$base/v0/management/plugins/antigravity-cloak/config" -Method Patch -Headers @{ Authorization = "Bearer $key"; "Content-Type" = "application/json" } -Body '{"model_prefixes":["agy"]}'
   ```

8. **Verify active plugin status**:
   ```powershell
   Invoke-RestMethod -Uri "$base/v0/management/plugins" -Headers @{ Authorization = "Bearer $key" }
   # Find antigravity-cloak with registered: true, effective_enabled: true, and path pointing at the v<ver> .so.
   ```
