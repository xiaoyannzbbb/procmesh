# Release Trust and Managed Installation

ProcMesh managed updates accept only stable release metadata signed by an
Ed25519 key compiled into the Agent and the official installer. SHA-256 still
appears in `checksums.txt` for manual inspection, but it is not an identity or
installation trust source.

## Trust chain

The release publishes four canonical JSON files:

- `stable.json` is the Channel Index. It identifies one stable version, expires,
  and pins the exact `manifest.json` URL and SHA-256 digest.
- `stable.json.sig` is its detached Ed25519 signature envelope.
- `manifest.json` declares protocol, Shim, rollback, artifact size/digest, and
  the fixed official GitHub Release URLs.
- `manifest.json.sig` is its detached Ed25519 signature envelope.

Verification order is index signature and expiry, fixed origin, manifest digest,
manifest signature, compatibility, artifact size/digest, then safe archive
extraction. Unknown schema fields, unknown key IDs, prereleases, arbitrary URLs,
absolute or traversing archive paths, links, devices, duplicate files, and
unexpected executable files are rejected before `current` changes.

The installer follows redirects itself. Every hop must remain HTTPS on the
fixed GitHub/release-assets host allowlist, with at most five redirects, fixed
connect/total/stall timeouts, and a per-file byte ceiling. Downloads are written
to a same-directory temporary file and published atomically only after curl and
the size bound succeed.

## Initial key ceremony

The repository deliberately does not contain a private release key. Before the
first managed release, the release owner must generate one on the signing host:

```bash
umask 077
openssl genpkey -algorithm ED25519 -out procmesh-release-2026.pem
openssl pkey -in procmesh-release-2026.pem -pubout -outform DER \
  | tail -c 32 | openssl base64 -A
```

Store the private PEM offline with the release credentials. Never commit it,
upload it as a release asset, pass it through an issue, or put it in a shell
trace. Add only the printed raw public key to
`internal/update/trust/trusted_keys.json`, using a stable lowercase `key_id`.
Copy the exact canonical registry JSON into `trusted_key_registry` in
`scripts/install.sh`. `TestInstallerAndUpdaterEmbedTheSameTrustedKeys` prevents
the two trust roots from drifting.

The checked-in registries are empty until this ceremony occurs. That state is
intentional and fail-closed: release metadata generation, installation, and the
updater cannot accept an artifact.

## Key rotation

Rotation requires an overlap release:

1. Generate the next offline key and add its public key while retaining the old
   key. Release an Agent containing both keys, signed by the old key.
2. Confirm supported managed nodes run a version containing both keys.
3. Sign later releases with the new key. Old Agents that do not contain it must
   be updated manually.
4. Remove the old public key only in a later stable release. Never reuse a
   retired `key_id` for different key material.

If the old private key is compromised before an overlap release, no automatic
rotation can establish trust. Publish the incident and require a manual trusted
bootstrap.

## Official release

The release command requires the signing key, its registered key ID, and the
versions to which rollback is safe:

```bash
export PROCMESH_RELEASE_SIGNING_KEY=/secure/procmesh-release-2026.pem
export PROCMESH_RELEASE_KEY_ID=2026-release
export PROCMESH_ROLLBACK_SAFE_FROM=v1.2.2
scripts/release.sh v1.2.3 --dry-run
scripts/release.sh v1.2.3
```

The metadata generator reads the private key only from a regular file with no
group/world permissions. It self-verifies the generated metadata against the
checked-in public registry before release upload. The signing file is never
copied into `dist/` or an archive.

## Managed Linux layout

The official installer creates this fixed layout:

```text
/usr/local/lib/procmesh/
  versions/v1.2.3/
    procmesh
    procmesh-agent
    procmesh-shim
    procmesh-updater
  current -> versions/v1.2.3
  previous -> versions/v1.2.3

/usr/local/bin/procmesh       -> ../lib/procmesh/current/procmesh
/usr/local/bin/procmesh-agent -> ../lib/procmesh/current/procmesh-agent
/usr/local/bin/procmesh-shim  -> ../lib/procmesh/current/procmesh-shim
```

The first U0 release is a manual bootstrap boundary. The installer refuses an
existing managed installation and any custom or partial flat layout before
changing binaries or units. For the recognized official flat layout, an
administrator must explicitly confirm bootstrap and enter the legacy SemVer.
The installer verifies root ownership, all three legacy binaries, the fixed
binary and Shim paths, `KillMode=process`, and signed rollback compatibility.
It preserves the old binaries and unit as a real previous version, keeps the
existing config and data paths, then switches and runs strict update health.
Failure restores the legacy pointer and unit before restarting the old Agent.
Existing managed installations apply releases only through `procmesh-updater`.

`procmesh-agent-update@.service` accepts only a canonical operation UUID and
reads root-private inputs below `/var/lib/procmesh/update/operations/<uuid>`.
It has no network listener and no URL, command, or path argument. The recovery
oneshot runs at boot and resumes durable non-terminal journals.

## Transaction and rollback

The updater verifies every input before staging. It writes and fsyncs a complete
immutable version directory, atomically replaces the `previous` and `current`
symlinks on the same filesystem, restarts only `procmesh-agent.service`, and
requires `/healthz`, `/readyz`, and `/updatez`, plus the systemd MainPID
executable to match the target version. `/updatez` reports the compiled Agent
version, SQLite readiness, and completion of initial/current Shim recovery. A
failed check restores `current` to `previous`, restarts the old Agent, and
checks it again.

Each immutable version directory carries a canonical marker containing the
schema, release version, signed manifest digest, and artifact digest. Operation
UUIDs are deliberately excluded, so a fresh operation may retry the identical
release after a health rollback without weakening release identity.

The durable phases are `STAGED`, `SWITCHED`, `RESTARTED`, and `HEALTHY`, followed
by `SUCCEEDED`. A failed target enters `ROLLING_BACK`, then `ROLLED_BACK` or
`ROLLBACK_FAILED`. Replaying a terminal journal does not switch or restart
again. Before staging, journal schema 2 records the original verification time,
signed manifest digest, and artifact digest. Recovery revalidates signatures at
that original time and compares the immutable identity, so an expired Channel
Index cannot strand a verified transaction while changed inputs force a
deterministic rollback.

Only ProcMesh binary pointers are rolled back. Configuration, SQLite, Process
Specs, runtime state, Shim processes, and business processes are never copied,
recreated, stopped, or killed by the updater. `KillMode=process` remains required
on the Agent unit so an Agent restart leaves existing Shims and business PIDs
running.

## Linux U0 acceptance

The destructive U0 acceptance test must run as root on a disposable Linux host
booted with systemd. It starts from the recognized flat layout, bootstraps it,
and proves the Shim and business PIDs survive the Agent switch. It then
exercises health rollback and updater termination at `STAGED`, `SWITCHED`,
`RESTARTED`, active health checking, and `HEALTHY`.

```bash
scripts/build-update-u0-fixture.sh ./u0-fixture amd64
sudo PROCMESH_RUN_UPDATE_U0=1 \
  PROCMESH_U0_ARTIFACTS="$PWD/u0-fixture" \
  scripts/test-update-u0-linux.sh
```

Cross-boot recovery must be run independently for the three specified power
loss windows: completed staging, completed symlink switch, and active new-Agent
health checking. `prepare` leaves the selected schema-2 `RUNNING` journal and
records its pointer, boot ID, Shim PID, and business PID. Force-stop the
disposable VM without an orderly guest shutdown, then start it again. The
enabled recovery unit completes the operation to `SUCCEEDED/HEALTHY`; `resume`
verifies the changed boot ID, target pointer, and current Shim takeover before
cleanup:

```bash
for window in STAGED SWITCHED HEALTH_CHECKING; do
  # Run each window on a fresh disposable VM.
  sudo PROCMESH_RUN_UPDATE_U0=1 PROCMESH_U0_CROSS_BOOT_STAGE=prepare \
    PROCMESH_U0_POWER_WINDOW="$window" scripts/test-update-u0-linux.sh
  # Force-stop and restart the VM from the host/hypervisor here; do not reboot
  # or shut down the guest cleanly.
  sudo PROCMESH_RUN_UPDATE_U0=1 PROCMESH_U0_CROSS_BOOT_STAGE=resume \
    scripts/test-update-u0-linux.sh
done
```

Use `arm64` for an arm64 Linux host. The `updateaccept` build tag embeds only a
public test key and enables a file-based process-exit checkpoint; neither is
present in a production updater build. The fixture builder does not retain the
test private key in its output.
