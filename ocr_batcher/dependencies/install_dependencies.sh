#!/usr/bin/env bash

set -Eeuo pipefail

if [[ ! -r /etc/os-release ]]; then
    echo "[ERROR] Cannot identify this operating system: /etc/os-release is unavailable." >&2
    exit 1
fi

# shellcheck source=/etc/os-release
. /etc/os-release

case "${ID:-}" in
    debian|ubuntu)
        ;;
    *)
        echo "[ERROR] This installer supports Debian and Ubuntu only (detected: ${ID:-unknown})." >&2
        exit 1
        ;;
esac

if ! command -v apt-get >/dev/null 2>&1; then
    echo "[ERROR] apt-get is required to install dependencies." >&2
    exit 1
fi

if (( EUID == 0 )); then
    APT_GET=(apt-get)
else
    if ! command -v sudo >/dev/null 2>&1; then
        echo "[ERROR] Run as root or install sudo to use this installer." >&2
        exit 1
    fi
    APT_GET=(sudo apt-get)
fi

PACKAGES=(
    bash
    coreutils
    findutils
    ghostscript
    ocrmypdf
    pngquant
    qpdf
    tesseract-ocr
    tmux
    unpaper
)

"${APT_GET[@]}" update

# Debian 13 and Ubuntu 24.04+ package the JBIG2 encoder; older supported
# releases can run OCRmyPDF without it, with less effective PDF compression.
if apt-cache show jbig2 >/dev/null 2>&1; then
    PACKAGES+=(jbig2)
else
    echo "[INFO] No jbig2 package is available; OCR will work with reduced optimization."
fi

"${APT_GET[@]}" install --yes "${PACKAGES[@]}"

REQUIRED_COMMANDS=(
    ocrmypdf
    tmux
    realpath
    sha256sum
    cmp
    find
    nproc
)

for command_name in "${REQUIRED_COMMANDS[@]}"; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        echo "[ERROR] Installation completed, but required command '$command_name' is unavailable." >&2
        exit 1
    fi
done

echo "[INFO] Bare-metal OCR dependencies are installed."
echo "[INFO] Start the OCR script from inside a tmux session."
