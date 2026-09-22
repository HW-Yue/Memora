# memora — Install, detect, and remove an instance

Part of the `memora` Skill. It is loaded on demand: `SKILL.md` holds the constraints and the index, this file holds the procedure.

## Product manual and Admin

When the user asks what Memora is, how the architecture works, how a read/write
flows through the engine, or how to use/troubleshoot the local Admin, read
[`references/product-manual.md`](./product-manual.md). It is the
stable product and operations guide; it must not be used as a substitute for
live MSQL discovery. Admin is a local, read-only observer on `127.0.0.1:3888`;
all facts and all mutations still come from the scoped daemon through MSQL. If the
instance's daemon is not running, the CLI starts it and says so on stderr — one
daemon per instance, so a running one is never restarted. You do not need to run
`daemon start` yourself before a query.
## Install once

Before the first Memora operation in a session, resolve this Skill's directory
and run its read-only detector:

```sh
/bin/sh "<skill-directory>/scripts/check.sh"
```

The detector has five states, and only the first means "go ahead":

- `ready` — the CLI and the daemon serving the Instance agree. Use it.
- `skewed` — the CLI and the running daemon are **different builds**. The daemon
  is what answers your statements, so you would be reading through an engine you
  did not choose: restart it (`memora daemon stop`, then any command — the CLI
  starts the matching daemon and says so on stderr) and re-run the detector
  before your first read.
- `unknown` — the daemon could not answer at all; the envelope carries `reason`
  (a file sandbox that cannot read the instance's lock file is the usual cause).
  Treat it as *not verified*, not as fine: re-run the detector with the access
  the daemon needs, and if it stays unknown, say so where you report the answer
  instead of presenting the reads as verified.
- `missing` and `unhealthy` — as described next.

If it reports `ready`, use the detected executable. If it reports `missing`,
do not download or install anything yet. Tell the user that the latest Memora
release is available from `https://github.com/HW-Yue/Memora/releases/latest`
(the verified installer resolves the newest stable tag automatically), show the
default binary destination `~/.local/bin/memora` and the user-level Instance
destination, then ask whether they want to download it manually or explicitly
authorize this Skill's verified installer. If it reports `unhealthy`, show the
bounded diagnostic and ask before replacing anything.

Only after explicit installation authorization, resolve this Skill's own
directory and run:

```sh
/bin/sh "<skill-directory>/scripts/install.sh" --yes
```

The bootstrap supports only macOS arm64/amd64. It resolves the newest stable
GitHub Release by default, verifies the exact SHA-256 entry and staged binary
version, and replaces an old binary only after verification. Pass
`--version MAJOR.MINOR.PATCH` to pin a specific release instead. A checksum,
archive, or version mismatch is a hard failure and must never fall back. Only
an unavailable Release may fall back to a fixed Go module tag or an explicit
local source directory. Do not ask for sudo, change the install script, bypass
`--yes`, or claim success until its idempotent init, daemon start, and doctor
checks finish. If offline without a local source tree and Go toolchain, report
the recoverable blocker.
## Practise on a throwaway instance

A host that needs to exercise a write path without touching the user's memory runs
it against a fresh instance, and the recipe is two commands:

```sh
export HOME=$(mktemp -d)   # a fresh HOME is a fresh instance directory
memora init
```

Everything the CLI then does — daemon socket, locks, logs, caches — stays under
that HOME, so the real instance is untouched. Cleanup is the destroy below, and
that needs the user's explicit instruction like any other destroy.

## Removing an instance

Deleting a Row or a leaf is a language operation with an archive behind it.
Deleting a whole **instance** is not in the language at all: it is
`memora instance destroy --data-dir <absolute path> --yes`, and it is
irreversible. Ask the user first, name the exact directory, and never point it at
an instance whose contents you have not shown them. There is no DROP for databases
or tables, so an instance you created for a test is cleaned up this way rather
than from inside a statement.
