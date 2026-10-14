#!/usr/bin/env bash

set -Eeuo pipefail

if [[ ! -r /etc/os-release ]]; then
    echo "[ERROR] Cannot identify this operating system: /etc/os-release is unavailable." >&2
    exit 1
fi

# shellcheck disable=SC1091
. /etc/os-release

if (( EUID == 0 )); then
    SUDO=()
else
    if ! command -v sudo >/dev/null 2>&1; then
        echo "[ERROR] Run as root or install sudo to use this installer." >&2
        exit 1
    fi
    SUDO=(sudo)
fi

case "${ID:-}" in
    debian|ubuntu)
        command -v apt-get >/dev/null 2>&1 || {
            echo "[ERROR] apt-get is required on Debian and Ubuntu." >&2
            exit 1
        }
        PACKAGES=(
            bash coreutils findutils ghostscript ocrmypdf pngquant qpdf
            tesseract-ocr tmux unpaper
        )
        "${SUDO[@]}" apt-get update
        # Debian 13 and Ubuntu 24.04+ package JBIG2; older releases can run
        # OCRmyPDF without it, with less effective PDF compression.
        if apt-cache show jbig2 >/dev/null 2>&1; then
            PACKAGES+=(jbig2)
        else
            echo "[INFO] No jbig2 package is available; OCR will work with reduced optimization."
        fi
        "${SUDO[@]}" apt-get install --yes "${PACKAGES[@]}"
        ;;
    opensuse*|sles)
        command -v zypper >/dev/null 2>&1 || {
            echo "[ERROR] zypper is required on openSUSE and SUSE Linux Enterprise." >&2
            exit 1
        }
        PACKAGES=(
            bash coreutils findutils ghostscript ocrmypdf pngquant qpdf
            tesseract-ocr tmux unpaper
        )
        "${SUDO[@]}" zypper --non-interactive refresh
        "${SUDO[@]}" zypper --non-interactive install "${PACKAGES[@]}"
        ;;
    fedora)
        command -v dnf >/dev/null 2>&1 || {
            echo "[ERROR] dnf is required on Fedora." >&2
            exit 1
        }
        PACKAGES=(
            bash coreutils findutils ghostscript ocrmypdf pngquant qpdf
            tesseract tesseract-langpack-eng tesseract-osd tmux unpaper
        )
        "${SUDO[@]}" dnf install --assumeyes "${PACKAGES[@]}"
        ;;
    rhel|rocky|almalinux|ol)
        command -v dnf >/dev/null 2>&1 || {
            echo "[ERROR] dnf is required on RHEL and compatible distributions." >&2
            exit 1
        }
        PACKAGES=(
            bash coreutils curl findutils ghostscript qpdf
            tesseract tesseract-langpack-eng tesseract-osd tmux unpaper
        )
        if [[ "${VERSION_ID%%.*}" == 9 ]]; then
            PACKAGES+=(pngquant)
        fi
        echo "[INFO] unpaper and (on RHEL 9) pngquant may require EPEL."
        "${SUDO[@]}" dnf install --assumeyes "${PACKAGES[@]}"
        UV_BIN="$(command -v uv || true)"
        if [[ -z "$UV_BIN" ]]; then
            UV_INSTALLER="$(mktemp)"
            trap 'rm -f -- "$UV_INSTALLER"' EXIT
            curl --fail --location --silent --show-error \
                https://astral.sh/uv/install.sh --output "$UV_INSTALLER"
            sh "$UV_INSTALLER"
            rm -f -- "$UV_INSTALLER"
            trap - EXIT
            UV_BIN="$HOME/.local/bin/uv"
        fi
        if [[ ! -x "$UV_BIN" ]]; then
            echo "[ERROR] uv installation succeeded, but uv is not executable: $UV_BIN" >&2
            exit 1
        fi
        "${SUDO[@]}" "$UV_BIN" tool install \
            --install-dir /opt/ocrmypdf/uv-tools \
            --bin-dir /usr/local/bin \
            ocrmypdf
        ;;
    arch|manjaro)
        if (( EUID == 0 )); then
            echo "[ERROR] On Arch, run this script as a regular user with sudo configured; AUR packages must not be built as root." >&2
            exit 1
        fi
        command -v pacman >/dev/null 2>&1 || {
            echo "[ERROR] pacman is required on Arch Linux and Manjaro." >&2
            exit 1
        }
        PACKAGES=(
            base-devel bash coreutils findutils git ghostscript pngquant qpdf
            tesseract tesseract-data-eng tesseract-data-osd tmux unpaper
        )
        "${SUDO[@]}" pacman -Syu --needed --noconfirm "${PACKAGES[@]}"
        AUR_BUILD_DIR="$(mktemp -d)"
        trap 'rm -rf -- "$AUR_BUILD_DIR"' EXIT
        git clone https://aur.archlinux.org/ocrmypdf.git "$AUR_BUILD_DIR/ocrmypdf"
        (
            cd "$AUR_BUILD_DIR/ocrmypdf"
            makepkg --syncdeps --install
        )
        rm -rf -- "$AUR_BUILD_DIR"
        trap - EXIT
        ;;
    *)
        echo "[ERROR] Unsupported Linux distribution: ${ID:-unknown}." >&2
        echo "[ERROR] Supported distributions: Debian/Ubuntu, openSUSE, Fedora, RHEL, and Arch Linux." >&2
        exit 1
        ;;
esac

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
