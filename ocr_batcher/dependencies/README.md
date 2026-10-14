# Bare-metal dependencies

Run `install_dependencies.sh` to install the runtime requirements for
[`src/ocr_sequencial.sh`](../src/ocr_sequencial.sh) on Debian or Ubuntu. The
installer uses `apt` and must be run as root or through `sudo`.

```bash
bash dependencies/install_dependencies.sh
```

This is for running the Bash script directly on the host. It does not install
Docker or API-host dependencies, and it does not install `shc`, which is only
needed to build the optional compiled binary.

## Direct runtime packages

The installer explicitly installs the packages below. Licenses and copyright
holders refer to the upstream projects; distribution patches and package
versions may differ.

| Package / component | Why it is needed | License | Copyright holder(s) |
| --- | --- | --- | --- |
| Bash (`bash`) | Runs the entry-point script. | GNU GPL-3.0-or-later | Free Software Foundation, Inc. and contributors |
| GNU Coreutils (`coreutils`) | Provides `realpath`, `sha256sum`, and standard file/process utilities used by the script. | GNU GPL-3.0-or-later | Free Software Foundation, Inc. and contributors |
| GNU Findutils (`findutils`) | Provides `find` for recursive file discovery. | GNU GPL-3.0-or-later | Free Software Foundation, Inc. and contributors |
| tmux (`tmux`) | Required by the native workflow for its progress and log panes. | ISC | Nicholas Marriott and contributors |
| OCRmyPDF (`ocrmypdf`) | Runs the PDF OCR and optimization workflow. | Mozilla Public License 2.0 | James R. Barlow and OCRmyPDF contributors |
| Tesseract OCR (`tesseract-ocr`) | Performs text recognition for OCRmyPDF. | Apache License 2.0 | Google Inc. and Tesseract contributors |
| Ghostscript (`ghostscript`) | PDF rendering and processing backend used by OCRmyPDF. | GNU AGPL-3.0-or-later | Artifex Software, Inc. |
| qpdf (`qpdf`) | PDF transformation and validation backend used by OCRmyPDF. | Apache License 2.0 (qpdf 11+); Artistic License 2.0 (older releases) | Jay Berkenbilt and contributors |
| unpaper (`unpaper`) | Image cleanup backend used by the script's `--clean` and `--clean-final` options. | GNU GPL-2.0-only | Jens Gulden, Diego Elio Pettenò, and contributors |
| pngquant (`pngquant`) | Optional image optimization used by OCRmyPDF. | GNU GPL-3.0 | Kornel Lesiński and contributors |
| JBIG2 encoder (`jbig2`, when available) | Improves compression at OCRmyPDF optimization level 3. | Apache License 2.0 | Adam Langley and contributors |

The JBIG2 encoder is included by the installer only when the distribution
provides the `jbig2` package. OCRmyPDF remains usable without it, but optimized
PDFs may be larger.

## Transitive packages

`apt` also installs OCRmyPDF's and these tools' transitive runtime libraries.
That dependency closure varies by Debian/Ubuntu release and repository version,
so it is not a fixed package list here. The installed package's
`/usr/share/doc/<package>/copyright` file records the license and copyright
information for the exact version on the machine. The complete distribution
copyright notices are also available in `/usr/share/doc`.

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
