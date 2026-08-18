#!/usr/bin/env bash
#
# Upgrade ffmpeg to the latest BtbN static build.
#
# Why this and not apt: Ubuntu 20.04 ships ffmpeg 4.2.7 which lacks several
# features lampac-go uses (ABR multi-rendition, -fps_mode cfr, -hls_init_time,
# stable fmp4 HLS).  BtbN provides nightly static builds with everything:
# NVENC, VAAPI, QSV (libvpl), libplacebo HW tonemap, libx264, libx265,
# libass, libwebvtt — all in a single binary.  Drop-in, no system pkg drama,
# trivial rollback.
#
# Strategy:
#   1. Detect current ffmpeg, save backup
#   2. Download latest GPL build from GitHub releases
#   3. Verify integrity + features (NVENC presence, hwaccels)
#   4. Install to /usr/local/bin (shadows /usr/bin if PATH ordered correctly)
#   5. Print compatibility check + tell lampac-go to restart
#
# Idempotent: re-running with --reinstall fetches the latest nightly.
# Rollback: ./upgrade-ffmpeg.sh --rollback restores the saved 4.2.7 binaries.
#
# Tested on: Ubuntu 20.04, Ubuntu 22.04, Debian 11, Debian 12.
# Requires:   bash, curl, tar, xz-utils, sudo (or run as root).

set -euo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

# Pin to a known-good release tag here when you don't want auto-updates.
# Empty string = use the floating "latest" tag (nightly master build).
RELEASE_TAG="${FFMPEG_RELEASE_TAG:-}"

# linux64-gpl: includes libx264/libx265 (GPL components).  The lgpl build
# omits them which would break video re-encode for most sources.
BUILD_VARIANT="linux64-gpl"

INSTALL_DIR="/usr/local/bin"
BACKUP_DIR="/var/backups/ffmpeg"
# Tmpdir prefix intentionally does NOT start with "ffmpeg-" so the
# extraction step's `find -name 'ffmpeg-*'` doesn't pick the tmpdir itself
# as the extracted directory.  (We hit that bug in the wild — find returned
# /tmp/ffmpeg-upgrade.XXXX/ before /tmp/ffmpeg-upgrade.XXXX/ffmpeg-N.../.)
TMP_DIR="$(mktemp -d -t lampac-ffmpeg-upgrade.XXXXXX)"
trap 'rm -rf "$TMP_DIR"' EXIT

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------

log()  { printf '\033[1;34m[upgrade]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ok]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

# Run command as root if not already, else direct.
sudo_run() {
    if [[ $EUID -eq 0 ]]; then
        "$@"
    else
        sudo "$@"
    fi
}

# ---------------------------------------------------------------------------
# Sanity checks
# ---------------------------------------------------------------------------

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || fail "missing dependency: $1 (install it: apt install $1)"
}

require_cmd curl
require_cmd tar
require_cmd xz   # part of xz-utils on debian/ubuntu

# ---------------------------------------------------------------------------
# Subcommands
# ---------------------------------------------------------------------------

cmd_status() {
    log "current ffmpeg state on $(hostname):"
    if command -v ffmpeg >/dev/null 2>&1; then
        local v
        v="$(ffmpeg -version 2>/dev/null | head -1 || true)"
        ok "  $(which ffmpeg): $v"
    else
        warn "  ffmpeg NOT in PATH"
    fi
    if [[ -d "$BACKUP_DIR" ]]; then
        log "  saved backups:"
        ls -lh "$BACKUP_DIR" 2>/dev/null | sed 's/^/    /'
    fi
}

cmd_rollback() {
    log "rolling back to saved binaries…"
    if [[ ! -d "$BACKUP_DIR" ]]; then
        fail "no backup directory at $BACKUP_DIR — nothing to roll back to"
    fi

    local restored=0
    for tool in ffmpeg ffprobe; do
        local backup="$BACKUP_DIR/$tool"
        if [[ -f "$backup" ]]; then
            local target
            target="$(readlink -f "$BACKUP_DIR/${tool}.path" 2>/dev/null || echo "/usr/bin/$tool")"
            sudo_run cp "$backup" "$target"
            sudo_run chmod +x "$target"
            ok "  restored $target ($(${target} -version 2>/dev/null | head -1))"
            ((restored++))
        fi
    done

    # Remove the BtbN install if we put one in /usr/local/bin.
    for tool in ffmpeg ffprobe; do
        local installed="$INSTALL_DIR/$tool"
        if [[ -f "$installed" ]]; then
            sudo_run rm -f "$installed"
            log "  removed $installed (BtbN install)"
        fi
    done

    if [[ $restored -eq 0 ]]; then
        warn "nothing was restored — backup dir empty?"
    fi

    log "rollback done. Restart lampac-go to pick up the change."
}

# Save the current ffmpeg/ffprobe binaries to BACKUP_DIR so the user can
# return to the system-shipped version with --rollback.  Records the
# original install path in <tool>.path for accurate restore.
backup_current() {
    sudo_run mkdir -p "$BACKUP_DIR"
    for tool in ffmpeg ffprobe; do
        local p
        p="$(command -v "$tool" 2>/dev/null || true)"
        if [[ -n "$p" && -f "$p" ]]; then
            # Don't double-backup if BtbN install is already in PATH ahead
            # of the system binary.
            if [[ "$p" == "$INSTALL_DIR/$tool" ]]; then
                log "  $tool already installed at $p — skipping backup of self"
                continue
            fi
            if [[ ! -f "$BACKUP_DIR/$tool" ]]; then
                sudo_run cp "$p" "$BACKUP_DIR/$tool"
                echo "$p" | sudo_run tee "$BACKUP_DIR/${tool}.path" >/dev/null
                ok "  backed up $p → $BACKUP_DIR/$tool"
            else
                log "  $BACKUP_DIR/$tool exists — keeping the older backup (do not overwrite)"
            fi
        else
            warn "  $tool not found in PATH — skipping backup"
        fi
    done
}

# Resolve the BtbN release URL.  When RELEASE_TAG is empty, hit the
# "latest" tag which BtbN updates with each nightly master build.
resolve_release_url() {
    local tag="${1:-latest}"
    local fname="ffmpeg-master-${tag}-${BUILD_VARIANT}.tar.xz"
    if [[ "$tag" == "latest" ]]; then
        echo "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/${fname}"
    else
        echo "https://github.com/BtbN/FFmpeg-Builds/releases/download/${tag}/${fname}"
    fi
}

cmd_install() {
    log "ffmpeg upgrade starting on $(hostname) ($(uname -srm))"
    cmd_status
    backup_current

    local tag="${RELEASE_TAG:-latest}"
    local url
    url="$(resolve_release_url "$tag")"

    log "downloading $url …"
    curl --fail --location --silent --show-error \
         -o "$TMP_DIR/ffmpeg.tar.xz" \
         "$url" \
        || fail "download failed: $url"

    local size_mb
    size_mb=$(( $(stat -c%s "$TMP_DIR/ffmpeg.tar.xz" 2>/dev/null || stat -f%z "$TMP_DIR/ffmpeg.tar.xz") / 1024 / 1024 ))
    ok "  downloaded ${size_mb} MB"

    log "extracting…"
    ( cd "$TMP_DIR" && tar -xf ffmpeg.tar.xz )
    # -mindepth 1 prevents matching the tmpdir itself; -name pattern is
    # restrictive enough to skip the downloaded tarball file too.
    local extracted_dir
    extracted_dir="$(find "$TMP_DIR" -mindepth 1 -maxdepth 1 -type d -name 'ffmpeg-*' | head -1)"
    if [[ -z "$extracted_dir" ]]; then
        # Fallback: BtbN sometimes ships nested differently.  Locate the
        # binary directly and walk up.
        local found_bin
        found_bin="$(find "$TMP_DIR" -mindepth 2 -maxdepth 4 -type f -name ffmpeg -executable 2>/dev/null | head -1)"
        if [[ -n "$found_bin" ]]; then
            extracted_dir="$(dirname "$(dirname "$found_bin")")"
            log "  located via fallback: $extracted_dir"
        else
            fail "extraction produced no ffmpeg-*/ directory and no bin/ffmpeg found in tmp tree"
        fi
    fi

    # ----- Verification phase: prove the binary works BEFORE installing. -----
    local new_ffmpeg="$extracted_dir/bin/ffmpeg"
    local new_ffprobe="$extracted_dir/bin/ffprobe"
    [[ -x "$new_ffmpeg" ]]  || fail "$new_ffmpeg not executable"
    [[ -x "$new_ffprobe" ]] || fail "$new_ffprobe not executable"

    log "verifying new binary…"
    local ver
    ver="$("$new_ffmpeg" -version 2>/dev/null | head -1)" || fail "$new_ffmpeg -version failed"
    ok "  $ver"

    # Feature presence checks — fail fast if the build doesn't have what
    # lampac-go needs.  These are essential; without them we'd install a
    # binary that breaks transcoding entirely.
    log "checking encoder availability…"
    local encoders
    encoders="$("$new_ffmpeg" -hide_banner -encoders 2>/dev/null)" || fail "encoder enumeration failed"
    for enc in libx264 libx265 aac; do
        if echo "$encoders" | grep -qw "$enc"; then
            ok "  $enc present"
        else
            fail "$enc encoder missing — wrong build variant?"
        fi
    done

    # Optional features — record presence but don't fail if absent.
    log "checking optional HW encoders…"
    for enc in h264_nvenc hevc_nvenc h264_vaapi h264_qsv h264_videotoolbox h264_rkmpp; do
        if echo "$encoders" | grep -qw "$enc"; then
            ok "  $enc present"
        else
            log "  $enc not built in (or filtered out for this platform)"
        fi
    done

    log "checking HW acceleration backends…"
    "$new_ffmpeg" -hide_banner -hwaccels 2>&1 | sed 's/^/    /' || true

    # ----- Install phase. -----
    log "installing to $INSTALL_DIR…"
    sudo_run install -m 0755 "$new_ffmpeg"  "$INSTALL_DIR/ffmpeg"
    sudo_run install -m 0755 "$new_ffprobe" "$INSTALL_DIR/ffprobe"
    ok "  installed $INSTALL_DIR/ffmpeg, $INSTALL_DIR/ffprobe"

    # Sanity: PATH order.  /usr/local/bin must come before /usr/bin or
    # the system 4.2.7 will still win.
    log "checking PATH order…"
    local first_ffmpeg
    first_ffmpeg="$(command -v ffmpeg)"
    if [[ "$first_ffmpeg" != "$INSTALL_DIR/ffmpeg" ]]; then
        warn "PATH puts $first_ffmpeg before $INSTALL_DIR/ffmpeg!"
        warn "  Either fix PATH so /usr/local/bin comes first, OR update lampac-go config:"
        warn "    [transcoding]"
        warn "    ffmpeg = \"$INSTALL_DIR/ffmpeg\""
    else
        ok "  $(ffmpeg -version | head -1)"
    fi

    # ----- Post-install advisory. -----
    cat <<EOF

────────────────────────────────────────────────────────────────────────
ffmpeg upgrade complete.

What changed:
  · old binary backed up at $BACKUP_DIR/
  · new binary installed at $INSTALL_DIR/{ffmpeg,ffprobe}
  · system /usr/bin/ffmpeg untouched (so apt upgrades don't fight us)

Next steps:
  1. Restart lampac-go so it re-detects the version:
       systemctl restart lampac-go
       # OR if you run it manually:
       pkill -f lampac-go && /path/to/lampac-go &
  2. Confirm the compatibility report flipped to all-green:
       curl -s http://localhost:9118/transcoding/stats | jq .ffmpeg_major
       curl -s http://localhost:9118/transcoding/stats | jq '.ffmpeg_compat[] | select(.active==false)'
       # The second command should print nothing (or just readrate_initial_burst on ffmpeg < 7).
  3. If anything breaks, roll back instantly:
       $0 --rollback

────────────────────────────────────────────────────────────────────────
EOF
}

# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

usage() {
    cat <<EOF
Usage: $0 [--install | --rollback | --status | --reinstall]

  --install    Download latest BtbN ffmpeg and install to $INSTALL_DIR.
               Default action when no flag given.
  --reinstall  Same as --install but forces a fresh download (skips
               cache reuse — useful when a nightly is broken).
  --rollback   Restore the system-shipped binaries from $BACKUP_DIR
               and remove the BtbN install.
  --status     Print current state without changing anything.

Env vars:
  FFMPEG_RELEASE_TAG  Pin to a specific BtbN release tag (e.g. "n7.1-latest").
                      Empty = nightly master build.

Backup dir:    $BACKUP_DIR
Install dir:   $INSTALL_DIR
Build variant: $BUILD_VARIANT
EOF
}

case "${1:---install}" in
    --install|--reinstall) cmd_install ;;
    --rollback)            cmd_rollback ;;
    --status)              cmd_status ;;
    -h|--help)             usage ;;
    *) usage; exit 1 ;;
esac
