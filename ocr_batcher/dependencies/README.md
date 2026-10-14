# Bare-metal dependencies

The bare-metal Bash script asks before running `install_dependencies.sh` if it
finds any required commands missing. You can also run it manually to install
the runtime requirements for
[`src/ocr_sequencial.sh`](../src/ocr_sequencial.sh) on Debian/Ubuntu, openSUSE,
Fedora, RHEL, or Arch Linux. The installer detects the distribution through
`/etc/os-release`, then uses `apt`, `zypper`, `dnf`, or `pacman` as appropriate.
Run it as root or through `sudo`, except on Arch where it must run as a
non-root user with `sudo` configured.

**Git and tmux must be available before the bare-metal OCR script can operate.**
The OCR workflow requires an existing tmux session and cannot run without
tmux. If tmux is not installed, run this dependency installer manually first;
then start a tmux session and run `src/ocr_sequencial.sh` inside it. Git is
required by the Arch installation path to fetch OCRmyPDF's AUR build recipe.

If you decline the prompt, or run the batcher without an interactive terminal,
the script exits and tells you to run the installer manually. Installation can
request administrator authentication and may upgrade packages (the Arch
`pacman -Syu` path). Unsupported distributions or failed package-manager
commands stop the batcher with an error.

```bash
bash dependencies/install_dependencies.sh
```

This is for running the Bash script directly on the host. It does not install
Docker or API-host dependencies, and it does not install `shc`, which is only
needed to build the optional compiled binary.

## Direct runtime packages

The installer explicitly installs the components below; their package names
vary by distribution. Licenses and copyright holders refer to the upstream
projects; distribution patches and package versions may differ.

| Package / component | Why it is needed | License | Copyright holder(s) |
| --- | --- | --- | --- |
| Bash (`bash`) | Runs the entry-point script. | GNU GPL-3.0-or-later | Free Software Foundation, Inc. and contributors |
| GNU Coreutils (`coreutils`) | Provides `realpath`, `sha256sum`, and standard file/process utilities used by the script. | GNU GPL-3.0-or-later | Free Software Foundation, Inc. and contributors |
| GNU Findutils (`findutils`) | Provides `find` for recursive file discovery. | GNU GPL-3.0-or-later | Free Software Foundation, Inc. and contributors |
| tmux (`tmux`) | Required by the native workflow for its progress and log panes. | ISC | Nicholas Marriott and contributors |
| OCRmyPDF (`ocrmypdf`) | Runs the PDF OCR and optimization workflow. | Mozilla Public License 2.0 | James R. Barlow and OCRmyPDF contributors |
| Tesseract OCR (`tesseract-ocr`, `tesseract`) | Performs text recognition for OCRmyPDF. | Apache License 2.0 | Google Inc. and Tesseract contributors |
| Tesseract orientation data (`tesseract-osd`, `tesseract-data-eng`) | Supplies orientation detection data and the default English recognition data where separately packaged. | Apache License 2.0 | Google Inc. and Tesseract contributors |
| Ghostscript (`ghostscript`) | PDF rendering and processing backend used by OCRmyPDF. | GNU AGPL-3.0-or-later | Artifex Software, Inc. |
| qpdf (`qpdf`) | PDF transformation and validation backend used by OCRmyPDF. | Apache License 2.0 (qpdf 11+); Artistic License 2.0 (older releases) | Jay Berkenbilt and contributors |
| unpaper (`unpaper`) | Image cleanup backend used by the script's `--clean` and `--clean-final` options. | GNU GPL-2.0-only | Jens Gulden, Diego Elio Pettenò, and contributors |
| pngquant (`pngquant`) | Optional image optimization used by OCRmyPDF. | GNU GPL-3.0 | Kornel Lesiński and contributors |
| JBIG2 encoder (`jbig2`, when available) | Improves compression at OCRmyPDF optimization level 3. | Apache License 2.0 | Adam Langley and contributors |

The JBIG2 encoder is installed on Debian/Ubuntu only when the distribution
provides the `jbig2` package. OCRmyPDF remains usable without it, but optimized
PDFs may be larger.

RHEL does not provide OCRmyPDF as a system package. The installer installs its
system dependencies with DNF, then installs `uv` using Astral's official
installer and installs OCRmyPDF with `uv tool` into `/opt/ocrmypdf` with its
command in `/usr/local/bin`. `unpaper` and, on RHEL 9, `pngquant` may require
EPEL to be enabled first; the installer does not enable additional repositories
automatically.

Arch does not provide OCRmyPDF in its official repositories. The installer
must be run as a regular user with `sudo` configured: it installs system
dependencies with `pacman`, then clones and builds the OCRmyPDF AUR package
with `makepkg`. The AUR package must not be built as root. The pacman step
uses `-Syu`, which synchronizes package databases and upgrades installed
packages before installing dependencies.

## Installer and build tools

These are used only to install OCRmyPDF on RHEL or build its AUR package on
Arch; they are not OCR runtime dependencies.

| Package / component | Where used | License | Copyright holder(s) |
| --- | --- | --- | --- |
| curl (`curl`) | Downloads Astral's uv installer on RHEL. | curl License (MIT/X derivate) | Daniel Stenberg and curl contributors |
| uv (`uv`) | Installs OCRmyPDF in an isolated tool environment on RHEL. | Apache License 2.0 or MIT | Astral Software Inc. and contributors |
| Git (`git`) | Fetches the OCRmyPDF AUR build recipe on Arch. | GNU GPL-2.0-only | Junio C. Hamano and Git contributors |
| `base-devel` package group | Provides the compiler/build tools needed by `makepkg` on Arch. | Varies by included package | Individual package copyright holders; see installed package metadata |

## Transitive packages

The selected package manager also installs OCRmyPDF's and these tools'
transitive runtime libraries. That dependency closure varies by distribution
release and repository version, so it is not a fixed package list here. The installed package's
`/usr/share/doc/<package>/copyright` file records the license and copyright
information for the exact version on Debian-family systems. On RPM- and
Arch-based systems, consult package metadata with `rpm -qi <package>` or
`pacman -Qi <package>` and the upstream license references below.

## Upstream license references

- [GNU Bash](https://www.gnu.org/software/bash/)
- [GNU Coreutils](https://www.gnu.org/software/coreutils/)
- [GNU Findutils](https://www.gnu.org/software/findutils/)
- [tmux](https://github.com/tmux/tmux)
- [OCRmyPDF](https://github.com/ocrmypdf/OCRmyPDF)
- [Tesseract OCR](https://github.com/tesseract-ocr/tesseract)
- [Ghostscript](https://ghostscript.com/)
- [qpdf](https://github.com/qpdf/qpdf)
- [unpaper](https://github.com/unpaper/unpaper)
- [pngquant](https://github.com/kornelski/pngquant)
- [jbig2enc](https://github.com/agl/jbig2enc)
