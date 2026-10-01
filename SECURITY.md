# Security

## Reporting a vulnerability

Please report privately through the "Report a vulnerability" button on the
Security tab of this repository (a private security advisory). Do not open a
public issue or pull request for a security problem. There is no contact
email. If the button is missing, open a plain issue that says only "security
contact needed", without details, and private reporting will be enabled.

Include the version (`hermes-safe-update version`), your OS, what you did and
what happened. Remove personal paths and tokens from anything you attach.
This is a one-person spare-time project, so there is no guaranteed response
time, but security reports are looked at before anything else. There is no
bounty program. Credit in the fix notes is fine; say so in the report.

Supported version: the latest release.

## What the tool touches

On your machine only, with your own permissions:

- Reads: the Hermes home folder (`gateway_state.json`, every profile's
  `state.db` read-only), the Hermes checkout (through git and `hermes`), the
  desktop app's data folder (`Local Storage`).
- Writes: `safe-update.json`, `logs/safe-update.log`,
  `logs/safe-update-timings.json`, the update-in-progress marker,
  `backups/settings-*`, `backups/settings-migrations.json` (marker),
  `cache/safe-update-gc.py`, `cache/safe-update-trash`,
  `cache/safe-update-settings`, `cache/update-mirror.lock`, and the desktop's
  `Local Storage` folder, but only through a verified swap with a backup and
  rollback (see docs/settings-migration.md); temporary `.hsu-*` work folders
  appear inside the desktop data folder during that swap. With a mirror:
  the mirror folder (a clone of the Hermes repository),
  `logs/update-mirror.log`, `scripts/hermes_safe_update_mirror.py` and one
  Hermes cron job (created by `mirror setup`).
- Deletes: after a verified update on Windows, dependency cleanup deletes old
  dependency generations (`installs/<install>/environments/<generation>`
  under the Hermes home). Locked files are renamed into `cache/safe-update-trash` instead.
- Runs: `hermes` (the user's own launcher), the git bundled with Hermes (or
  git from PATH), Hermes' managed Python for two embedded helper scripts,
  Hermes' bundled Electron for the settings migrator, `schtasks`, and, only
  when a cua-driver logon task must be re-registered (never with
  `--unattended` or `--no-elevate`), one elevated PowerShell started through
  a Windows UAC prompt.
- Your hooks: commands from `safe-update.yaml`, run as an argument list with
  your permissions. Only put commands there that you trust. Anyone who can
  edit your `safe-update.yaml` can already run code as you.

## Network and privacy

No telemetry, no analytics, no crash upload, no update check of its own. The
tool contacts GitHub only to ask whether Hermes has new commits (the public
REST API, unauthenticated, or git through Hermes' own checkout), and for the
optional mirror. Logs stay on your machine; home and app-data paths in them
are replaced by `~` and `%LOCALAPPDATA%`. Settings values (which can contain
session titles) are never written to logs or reports; only key names are.

## Releases

Binaries are built with `-trimpath` and published with a `SHA256SUMS` file.
They are not code-signed, so Windows SmartScreen will warn. Verify the hash.
