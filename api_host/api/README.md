# API contract

The [versioned OpenAPI document](api_specification/main_api_specification_api_host.json)
defines job submission, status, cancellation, resume, result download, deletion,
cleanup, and health endpoints. For most clients, upload a `.tar.gz` archive
using multipart form data; shared-directory requests require
`ALLOW_DIRECTORY_ACCESS=true` and are disabled by default.
