# Vendored jattach binaries

These are the jattach binaries jdebug installs into a pod (tier 2). They are
**pinned and committed to this repo** — jdebug does NOT download jattach at
runtime.

- Upstream: https://github.com/jattach/jattach
- Pinned version: **v2.2** (tag)
- Source: `src/posix/*.c` fetched from `raw.githubusercontent.com/jattach/jattach/v2.2`
- Built: `gcc -O3 -static` (x64) and `aarch64-linux-gnu-gcc -O3 -static` (arm64),
  then `strip`. Statically linked on purpose so the same binary runs on both
  glibc and musl (alpine) pods.
- `-DJATTACH_VERSION="2.2"`

## Files
- `jattach-linux-x64`    — for pods reporting `uname -m` = x86_64 / amd64
- `jattach-linux-arm64`  — for pods reporting `uname -m` = aarch64 / arm64

## Checksums
See `SHA256SUMS` in this directory.

## Refreshing / replacing
To use the official upstream prebuilt binary instead, drop it in here under the
same filename, or pass `--binary /path/to/jattach` (or set `$JATTACH_BINARY`)
at runtime — both bypass the vendored copy.
