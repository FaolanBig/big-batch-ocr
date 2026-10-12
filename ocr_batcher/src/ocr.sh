#!/usr/bin/env bash

### !!! DEPRECATED !!! #####################################
### This script is deprecated and will be removed in the ###
### future. Please use scr/ocr_sequencial.sh instead.    ###
############################################################

##########################
### Search for README  ###
###  in this file for  ###
### important comments ###
##########################

### DEPENDENCIES ######################################
# OCRmyPDF https://github.com/ocrmypdf/ocrmypdf       #
# GNU parallel https://www.gnu.org/software/parallel/ #
# jbig2                                               #
# jbig2enc                                            #
#######################################################

set -euo pipefail

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <source_dir> <target_dir>"
    exit 1
fi

SOURCE_DIR="$(realpath "$1")"

mkdir -p "$2"
TARGET_DIR="$(realpath "$2")"

[[ -d "$SOURCE_DIR" ]] || {
    echo "Source directory does not exist: $SOURCE_DIR"
    exit 1
}

case "$TARGET_DIR" in
    "$SOURCE_DIR"|"$SOURCE_DIR"/*)
        echo "ERROR: Target directory must not be inside source directory."
        exit 1
        ;;
esac

for cmd in find parallel ocrmypdf nproc realpath; do
    command -v "$cmd" >/dev/null 2>&1 || {
        echo "Required command not found: $cmd"
        exit 1
    }
done

CPU_COUNT=$(nproc)

if (( CPU_COUNT <= 2 )); then
    JOBS=1
else
    JOBS=$((CPU_COUNT / 2))
fi

echo "Source directory : $SOURCE_DIR"
echo "Target directory : $TARGET_DIR"
echo "CPU cores        : $CPU_COUNT"
echo "Parallel jobs    : $JOBS"
echo "File count: $(ls -1R $SOURCE_DIR | wc -l)"
echo ""

read -p "Press [ENTER] to continue" # README comment out when auto launching the script

process_pdf() {
    local src="$1"
    local rel
    local dst

    rel="${src#$SOURCE_DIR/}"
    dst="$TARGET_DIR/$rel"

    mkdir -p "$(dirname "$dst")"

    echo "Processing: $src"

    if ! ocrmypdf \
        --skip-text \
        --rotate-pages \
        --deskew \
        --optimize 3 \
        -l deu+eng \
        --output-type pdf \
        "$src" \
        "$dst"
    then
        local rc=$?

        echo "ERROR ($rc): $src" >&2

        rm -f "$dst"

        return 1
    fi
}

export SOURCE_DIR
export TARGET_DIR
export -f process_pdf

find "$SOURCE_DIR" \
    -type f \
    -iname '*.pdf' \
    -print0 |
parallel \
    -0 \
    -j "$JOBS" \
    --bar \
    --line-buffer \
    --joblog "$TARGET_DIR/ocr-joblog.txt" \
    process_pdf {}

echo "OCR processing completed."
