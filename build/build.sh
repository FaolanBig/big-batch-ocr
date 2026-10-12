#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
SOURCE="$REPO_ROOT/src/ocr_sequencial.sh"
OUTPUT_DIR="$REPO_ROOT/bin"
TEMP_DIR=""
SHEBANG_CHANGED=0

restore_shebang() {
    if (( SHEBANG_CHANGED )); then
        if ! sed -i '1s|^#!/bin/bash$|#!/usr/bin/env bash|' "$SOURCE"; then
            echo "[ERROR] Failed to restore the script shebang." >&2
            return 1
        fi
        SHEBANG_CHANGED=0
    fi
}

cleanup() {
    local exit_code=$?
    trap - EXIT

    if ! restore_shebang; then
        exit_code=1
    fi

    if [[ -n "$TEMP_DIR" ]]; then
        rm -rf -- "$TEMP_DIR" || exit_code=1
    fi

    rm -rf "$REPO_ROOT/src/ocr_sequencial.sh.x.c" || exit_code=1
    echo "[INFO] Cleaned up temporary files"

    exit "$exit_code"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if ! command -v shc >/dev/null 2>&1; then
    echo "[ERROR] shc is required to build the binary." >&2
    exit 1
fi

if [[ ! -f "$SOURCE" ]]; then
    echo "[ERROR] Source script not found: $SOURCE" >&2
    exit 1
fi

IFS= read -r first_line < "$SOURCE"
if [[ "$first_line" != '#!/usr/bin/env bash' ]]; then
    echo "[ERROR] Unexpected source shebang: $first_line" >&2
    exit 1
fi

TEMP_DIR="$(mktemp -d)"
SHEBANG_CHANGED=1
sed -i '1s|^#!/usr/bin/env bash$|#!/bin/bash|' "$SOURCE"

shc -f "$SOURCE" -o "$TEMP_DIR/ocr_sequencial"

restore_shebang
mkdir -p -- "$OUTPUT_DIR"
mv -f -- "$TEMP_DIR/ocr_sequencial" "$OUTPUT_DIR/ocr_sequencial"

echo "[INFO] Built $OUTPUT_DIR/ocr_sequencial"
