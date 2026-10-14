#!/usr/bin/env bash

set -Eeuo pipefail

##########################
### Search for README  ###
###  in this file for  ###
### important comments ###
##########################

####### DEPENDENCIES ########
### tmux                  ###
### ocrmypdf              ###
### jbig2                 ###
### jbig2enc              ###
#############################

#####################
### Configuration ###
#####################

SCRIPT_NAME="${0##*/}"
SCRIPT_PATH="${BASH_SOURCE[0]}"
if [[ "$SCRIPT_PATH" == */* ]]; then
    SCRIPT_DIR="${SCRIPT_PATH%/*}"
else
    SCRIPT_DIR="."
fi
DEPENDENCY_INSTALLER="$SCRIPT_DIR/../dependencies/install_dependencies.sh"

SOURCE_DIR="${1:-}"
TARGET_DIR="${2:-}"
CURRENT_TMP=""
CURRENT_MARKER_TMP=""
FILE_LIST_TMP=""
PROGRESS_PANE=""
LOG_PANE=""

#################
### Functions ###
#################

die() {
    echo "[ERROR] $*" >&2
    exit 1
}

update_status() {
    local now elapsed remaining eta percent

    now=$(date +%s)
    elapsed=$((now - START_TIME))

    remaining=$((TOTAL_FILES - PROCESSED_FILES))

    if (( PROCESSED_FILES > 0 )); then
        eta=$((elapsed * remaining / PROCESSED_FILES))
    else
        eta=0
    fi

    if (( TOTAL_FILES > 0 )); then
        percent=$((PROCESSED_FILES * 100 / TOTAL_FILES))
    else
        percent=100
    fi

    cat > "$STATUS_FILE" <<EOF
Progress:         ${percent}%

Total files:      $TOTAL_FILES
Processed files:  $PROCESSED_FILES
Remaining files:  $remaining

PDFs total:       $TOTAL_PDFS
PDFs processed:   $PROCESSED_PDFS
PDFs remaining:   $((TOTAL_PDFS - PROCESSED_PDFS))
PDFs failed:      ${FAILED_PDFS:-0}

Elapsed:          ${elapsed}s
ETA:              ${eta}s

Current file:     ${CURRENT_FILE:-}
EOF
}

cleanup() {
    if [[ -n "${PROGRESS_PANE:-}" ]]; then
        tmux kill-pane -t "$PROGRESS_PANE" >/dev/null 2>&1 || true
    fi
    if [[ -n "${LOG_PANE:-}" ]]; then
        tmux kill-pane -t "$LOG_PANE" >/dev/null 2>&1 || true
    fi
    if [[ -n "${CURRENT_TMP:-}" ]]; then
        rm -f -- "$CURRENT_TMP"
    fi
    if [[ -n "${CURRENT_MARKER_TMP:-}" ]]; then
        rm -f -- "$CURRENT_MARKER_TMP"
    fi
    if [[ -n "${STATUS_FILE:-}" ]]; then
        rm -f -- "$STATUS_FILE"
    fi
    if [[ -n "${FILE_LIST_TMP:-}" ]]; then
        rm -f -- "$FILE_LIST_TMP"
    fi
}

marker_id_for_path() {
    local digest
    digest=$(printf '%s' "$1" | sha256sum) || return 1
    printf '%s' "${digest%% *}"
}

handle_signal() {
    local signal_name="$1"
    local exit_code="$2"

    echo "[INTERRUPTED] Received SIG${signal_name} while processing: ${CURRENT_FILE:-unknown}" \
        | tee -a "$ERROR_LOG" >&2
    exit "$exit_code"
}

trap cleanup EXIT

##################
### Validation ###
##################

if [[ -z "$SOURCE_DIR" || -z "$TARGET_DIR" ]]; then
    die "Usage: $SCRIPT_NAME <source_dir> <target_dir>"
fi

REQUIRED_COMMANDS=(
    ocrmypdf tmux realpath sha256sum cmp find nproc
    cat cp date dirname mkdir mktemp mv rm sleep tail tee touch
)
MISSING_COMMANDS=()
for command_name in "${REQUIRED_COMMANDS[@]}"; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        MISSING_COMMANDS+=("$command_name")
    fi
done

if (( ${#MISSING_COMMANDS[@]} > 0 )); then
    if [[ ! -x "$DEPENDENCY_INSTALLER" ]]; then
        die "Missing dependencies (${MISSING_COMMANDS[*]}) and installer not found or not executable: $DEPENDENCY_INSTALLER"
    fi
    echo "[INFO] Missing dependencies: ${MISSING_COMMANDS[*]}"
    if ! exec {TTY_FD}<>/dev/tty 2>/dev/null; then
        die "Cannot ask for permission to install dependencies without a terminal. Run '$DEPENDENCY_INSTALLER' manually, then retry."
    fi
    if ! read -r -p "Run the distro-specific dependency installer now? [y/N] " INSTALL_DEPENDENCIES <&"$TTY_FD"; then
        exec {TTY_FD}>&-
        die "Could not read your response. Run '$DEPENDENCY_INSTALLER' manually, then retry."
    fi
    exec {TTY_FD}>&-
    case "$INSTALL_DEPENDENCIES" in
        [yY]|[yY][eE][sS])
            ;;
        *)
            die "Dependency installation declined. Run '$DEPENDENCY_INSTALLER' manually, then retry."
            ;;
    esac
    echo "[INFO] Running the distro-specific dependency installer: $DEPENDENCY_INSTALLER"
    "$DEPENDENCY_INSTALLER" || die "Dependency installation failed."

    MISSING_COMMANDS=()
    for command_name in "${REQUIRED_COMMANDS[@]}"; do
        if ! command -v "$command_name" >/dev/null 2>&1; then
            MISSING_COMMANDS+=("$command_name")
        fi
    done
    if (( ${#MISSING_COMMANDS[@]} > 0 )); then
        die "Dependency installation completed, but these commands remain unavailable: ${MISSING_COMMANDS[*]}"
    fi
fi

SOURCE_DIR="$(realpath "$SOURCE_DIR")"
TARGET_DIR="$(realpath -m "$TARGET_DIR")"
#CPU_THREADS="${OCR_JOBS:-2}"
#export CPU_THREADS="${OCR_JOBS:-2}"
#export CPU_THREADS="${OCR_JOBS:-CPU_THREADS=$(($(nproc --all) / 2))}"
export CPU_THREADS="${OCR_JOBS:-$(($(nproc --all) * 3 / 4))}"


[[ "$CPU_THREADS" =~ ^[1-9][0-9]*$ ]] \
    || die "OCR_JOBS must be a positive integer."

[[ -d "$SOURCE_DIR" ]] \
    || die "Source directory does not exist."

if [[ "$SOURCE_DIR" == "$TARGET_DIR" ]]; then
    die "Source and target directory must be different."
fi

if [[ "$SOURCE_DIR" == "/" || "$TARGET_DIR" == "$SOURCE_DIR/"* ]]; then
    die "Target directory must not be located inside source directory."
fi

mkdir -p "$TARGET_DIR"
STATE_DIR="$TARGET_DIR/.ocr_sequencial_state"
mkdir -p "$STATE_DIR"

#####################
### tmux handling ###
#####################

if [[ -z "${TMUX:-}" ]]; then
    die "This script must be started from inside a tmux session."
fi

RUN_ID="$(date +%Y%m%d_%H%M%S)_$$"

STATUS_FILE="/tmp/ocr_status_${RUN_ID}"
OCR_LOG="/tmp/ocr_output_${RUN_ID}.log"
ERROR_LOG="/tmp/ocr_errors_${RUN_ID}.log"

touch "$STATUS_FILE" "$OCR_LOG" "$ERROR_LOG"
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
trap 'handle_signal HUP 129' HUP

CURRENT_PANE="$(tmux display-message -p "#{pane_id}")"

####################
### Create panes ###
####################

PROGRESS_PANE=$(
    tmux split-window \
        -v \
        -l 20 \
        -P \
        -F "#{pane_id}"
)

tmux select-pane -t "$CURRENT_PANE"

LOG_PANE=$(
    tmux split-window \
        -h \
        -l 120 \
        -P \
        -F "#{pane_id}"
)

#####################
### Progress pane ###
#####################

tmux send-keys -t "$PROGRESS_PANE" "
while true; do
    clear
    echo '==== OCR Progress ===='
    echo
    cat '$STATUS_FILE' 2>/dev/null || true
    sleep 1
done
" C-m

################
### Log pane ###
################

tmux send-keys -t "$LOG_PANE" "
tail -n 50 -F '$OCR_LOG'
" C-m

#######################
### Build file list ###
#######################

echo "[INFO] Scanning source directory ..."

FILE_LIST_TMP=$(mktemp)
if ! find "$SOURCE_DIR" -type f -print0 > "$FILE_LIST_TMP"; then
    die "Failed to scan source directory: $SOURCE_DIR"
fi

mapfile -d '' -t ALL_FILES < "$FILE_LIST_TMP"
PDF_FILES=()
for file in "${ALL_FILES[@]}"; do
    if [[ "$file" =~ \.[Pp][Dd][Ff]$ ]]; then
        PDF_FILES+=("$file")
    fi
done
rm -f -- "$FILE_LIST_TMP"
FILE_LIST_TMP=""

TOTAL_FILES=${#ALL_FILES[@]}
TOTAL_PDFS=${#PDF_FILES[@]}

PROCESSED_FILES=0
PROCESSED_PDFS=0
FAILED_PDFS=0
FAILED_FILES=0

START_TIME=$(date +%s)

update_status

##########################
### Copy non-PDF files ###
##########################

echo "[INFO] Copying non-PDF files ..."

for file in "${ALL_FILES[@]}"; do

    rel="${file#"$SOURCE_DIR"/}"
    out="$TARGET_DIR/$rel"

    CURRENT_FILE="$rel"

    if [[ "$file" =~ \.[Pp][Dd][Ff]$ ]]; then
        continue
    fi

    if [[ -f "$out" ]] && cmp -s -- "$file" "$out"; then
        echo "[SKIP] $rel"
        ((PROCESSED_FILES+=1))
        update_status
        continue
    fi

    if [[ -d "$out" ]] || ! mkdir -p -- "$(dirname "$out")"; then
        echo "[ERROR] Could not prepare destination: $out" | tee -a "$ERROR_LOG"
        ((FAILED_FILES+=1))
        ((PROCESSED_FILES+=1))
        update_status
        continue
    fi

    copy_tmp="${out}.copy-${RUN_ID}.tmp"
    CURRENT_TMP="$copy_tmp"
    if cp -a -- "$file" "$copy_tmp" && mv -f -- "$copy_tmp" "$out"; then
        CURRENT_TMP=""
    else
        rc=$?
        echo "[ERROR] Failed to copy: $file (exit code=$rc)" | tee -a "$ERROR_LOG"
        rm -f -- "$copy_tmp"
        CURRENT_TMP=""
        ((FAILED_FILES+=1))
    fi

    ((PROCESSED_FILES+=1))
    update_status
done

################
### OCR PDFs ###
################

echo "[INFO] Starting OCR processing ..."

for pdf in "${PDF_FILES[@]}"; do

    rel="${pdf#"$SOURCE_DIR"/}"
    out="$TARGET_DIR/$rel"
    tmp_out="${out}.ocr-${RUN_ID}.tmp.pdf"
    CURRENT_FILE="$rel"
    marker_id=$(marker_id_for_path "$rel") || {
        echo "[ERROR] Could not create completion marker id: $rel" | tee -a "$ERROR_LOG"
        ((FAILED_PDFS+=1))
        ((PROCESSED_FILES+=1))
        ((PROCESSED_PDFS+=1))
        update_status
        continue
    }
    marker="$STATE_DIR/$marker_id"
    marker_tmp="${marker}.tmp.$$"
    source_hash=$(sha256sum -- "$pdf") || {
        echo "[ERROR] Could not calculate source checksum: $pdf" | tee -a "$ERROR_LOG"
        ((FAILED_PDFS+=1))
        ((PROCESSED_FILES+=1))
        ((PROCESSED_PDFS+=1))
        update_status
        continue
    }
    source_signature="$SOURCE_DIR/$rel:${source_hash%% *}"

    if ! mkdir -p -- "$(dirname "$out")"; then
        echo "[ERROR] Could not prepare destination: $out" | tee -a "$ERROR_LOG"
        ((FAILED_PDFS+=1))
        ((PROCESSED_FILES+=1))
        ((PROCESSED_PDFS+=1))
        update_status
        continue
    fi

    marker_matches=0
    if [[ -f "$out" && -f "$marker" ]]; then
        if marker_signature=$(cat -- "$marker" 2>/dev/null); then
            if [[ "$marker_signature" == "$source_signature" ]]; then
                marker_matches=1
            fi
        else
            echo "[WARN] Could not read completion marker; reprocessing: $rel" \
                | tee -a "$ERROR_LOG"
        fi
    fi

    if (( marker_matches )); then
        echo "[SKIP] $rel"

        ((PROCESSED_FILES+=1))
        ((PROCESSED_PDFS+=1))

        update_status
        continue
    fi

    if [[ -f "$out" ]]; then
        echo "[INFO] Reprocessing unverified output: $rel"
    fi

    CURRENT_TMP="$tmp_out"
    CURRENT_MARKER_TMP="$marker_tmp"

    {
        echo
        echo "================================================="
        echo "START: $(date)"
        echo "FILE : $rel"
        echo "================================================="
        echo
    } >> "$OCR_LOG"

    if ocrmypdf \
        --rotate-pages \
        --deskew \
        --clean \
        --clean-final \
        --optimize 3 \
	--output-type pdf \
        --skip-text \
	--jobs "$CPU_THREADS" \
        "$pdf" \
        "$tmp_out" >> "$OCR_LOG" 2>&1
    then
        if mv -f -- "$tmp_out" "$out"; then
            if printf '%s\n' "$source_signature" > "$marker_tmp" \
                && mv -f -- "$marker_tmp" "$marker"; then
                CURRENT_TMP=""
                CURRENT_MARKER_TMP=""
                echo "[SUCCESS] completed OCR for $pdf"
            else
                rc=$?
                echo "[ERROR] Could not save completion marker: $pdf (exit code=$rc)" \
                    | tee -a "$ERROR_LOG"
                rm -f -- "$marker_tmp"
                CURRENT_TMP=""
                CURRENT_MARKER_TMP=""
                ((FAILED_PDFS+=1))
            fi
        else
            rc=$?
            echo "[ERROR] Failed to publish OCR output: $pdf (exit code=$rc)" \
                | tee -a "$ERROR_LOG"
            rm -f -- "$tmp_out"
            CURRENT_TMP=""
            CURRENT_MARKER_TMP=""
            ((FAILED_PDFS+=1))
        fi
    else
        rc=$?

        echo "[ERROR] OCR failed: $pdf (exit code=$rc)" \
            | tee -a "$ERROR_LOG"
        rm -f -- "$tmp_out"
        CURRENT_TMP=""
        CURRENT_MARKER_TMP=""
        ((FAILED_PDFS+=1))

    fi

    ((PROCESSED_FILES+=1))
    ((PROCESSED_PDFS+=1))

    update_status

done

################
### Finished ###
################

END_TIME=$(date +%s)

if (( FAILED_PDFS > 0 || FAILED_FILES > 0 )); then
    JOB_RESULT="Job completed with errors."
else
    JOB_RESULT="Job completed successfully."
fi

cat > "$STATUS_FILE" <<EOF
$JOB_RESULT

Total files:      $TOTAL_FILES
Processed files:  $PROCESSED_FILES

PDFs total:       $TOTAL_PDFS
PDFs processed:   $PROCESSED_PDFS
PDFs failed:      $FAILED_PDFS
Other files failed: $FAILED_FILES

Runtime:          $((END_TIME - START_TIME))s

OCR Log:          $OCR_LOG
Error Log:        $ERROR_LOG
EOF

echo
echo "[INFO] Finished."
echo "[INFO] Runtime: $((END_TIME - START_TIME))s"
echo "[INFO] OCR log  : $OCR_LOG"
echo "[INFO] Error log: $ERROR_LOG"

if (( FAILED_PDFS > 0 || FAILED_FILES > 0 )); then
    echo "[ERROR] Finished with $FAILED_PDFS failed PDF(s) and $FAILED_FILES other file error(s)."
    exit 1
fi
