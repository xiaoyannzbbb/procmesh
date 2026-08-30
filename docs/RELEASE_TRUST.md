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

The first U0 release is a manual bootstrap boundary. Existing flat binaries can
be replaced only after the installer verifies the signed release and the
administrator confirms the migration. Nodes without this layout and both
updater units are not managed-update capable.

`procmesh-agent-update@.service` accepts only a canonical operation UUID and
reads root-private inputs below `/var/lib/procmesh/update/operations/<uuid>`.
It has no network listener and no URL, command, or path argument. The recovery
oneshot runs at boot and resumes durable non-terminal journals.

## Transaction and rollback

The updater verifies every input before staging. It writes and fsyncs a complete
immutable version directory, atomically replaces the `previous` and `current`
symlinks on the same filesystem, restarts only `procmesh-agent.service`, and
requires `/healthz`, `/readyz`, plus the systemd MainPID executable to match the
target version. A failed check restores `current` to `previous`, restarts the old
Agent, and checks it again.

The durable phases are `STAGED`, `SWITCHED`, `RESTARTED`, and `HEALTHY`, followed
by `SUCCEEDED`. A failed target enters `ROLLING_BACK`, then `ROLLED_BACK` or
`ROLLBACK_FAILED`. Replaying a terminal journal does not switch or restart
again. Recovery after an interrupted durable phase reconciles the journal with
the actual symlink state and continues deterministically.

Only ProcMesh binary pointers are rolled back. Configuration, SQLite, Process
Specs, runtime state, Shim processes, and business processes are never copied,
recreated, stopped, or killed by the updater. `KillMode=process` remains required
on the Agent unit so an Agent restart leaves existing Shims and business PIDs
running.
