{
  secure_tunnel: true,
  title: 'Big Batch OCR',

  connection: {
    fields: [
      {
        name: 'base_url',
        label: 'API host URL',
        control_type: 'url',
        type: 'string',
        optional: false,
        hint: 'The origin of the API host, for example https://ocr.example.com'
      },
      {
        name: 'api_key',
        label: 'Bearer API key',
        control_type: 'password',
        type: 'string',
        optional: true,
        hint: 'Leave blank only if the API host operator enabled unauthenticated access.'
      }
    ],

    authorization: {
      type: 'custom_auth',
      apply: lambda do |connection|
        unless connection['api_key'].to_s.empty?
          headers(Authorization: "Bearer #{connection['api_key']}")
        end
      end
    },
    base_uri: lambda do |connection|
      base_url = connection['base_url'].to_s.strip
      base_url = "https://#{base_url}" unless base_url.match?(/\Ahttps?:\/\//i)
      base_url.sub(%r{/+\z}, '')
    end
  },

  test: lambda do |_connection|
    get('/health')
  end,

  object_definitions: {
    job_status: {
      fields: lambda do
        [
          { name: 'jobId', type: 'string', label: 'Job ID' },
          {
            name: 'status',
            type: 'string',
            label: 'Status',
            control_type: 'select',
            pick_list: [
              %w[queued queued],
              %w[starting starting],
              %w[running running],
              %w[cancelling cancelling],
              %w[succeeded succeeded],
              %w[failed failed],
              %w[cancelled cancelled]
            ]
          },
          { name: 'inputPath', type: 'string', label: 'Input path' },
          { name: 'outputPath', type: 'string', label: 'Output path' },
          { name: 'createdAt', type: 'string', label: 'Created at' },
          { name: 'startedAt', type: 'string', label: 'Started at' },
          { name: 'completedAt', type: 'string', label: 'Completed at' },
          { name: 'exitCode', type: 'integer', label: 'Exit code' },
          {
            name: 'error',
            type: 'object',
            properties: [
              { name: 'code', type: 'string' },
              { name: 'message', type: 'string' }
            ]
          },
          { name: 'statusUrl', type: 'string', label: 'Status URL' }
        ]
      end
    }
  },

  actions: {
    check_health: {
      title: 'Check API health',
      description: 'Check whether the API host is responsive.',

      execute: lambda do |_connection, _input|
        get('/health')
      end,

      output_fields: lambda do
        [{ name: 'status', type: 'string' }]
      end
    },

    submit_directory_job: {
      title: 'Submit a directory OCR job',
      description: 'Submit an OCR job using directories under the API host data root. Directory access must be enabled by the API host operator.',

      input_fields: lambda do
        [
          { name: 'inputPath', label: 'Input directory', type: 'string', optional: false },
          { name: 'outputPath', label: 'Output directory', type: 'string', optional: false },
          {
            name: 'mode',
            label: 'Execution mode',
            type: 'string',
            control_type: 'select',
            pick_list: [%w[Asynchronous asynchronous], %w[Synchronous synchronous]],
            default: 'asynchronous',
            optional: false
          },
          {
            name: 'createDestination',
            label: 'Create destination if missing',
            type: 'boolean',
            control_type: 'checkbox',
            default: false,
            optional: true
          },
          {
            name: 'language',
            label: 'OCR language(s)',
            type: 'string',
            optional: true,
            hint: 'Up to 128 characters; language codes separated by +, for example deu+eng. Leave blank to use the API host default.'
          },
          {
            name: 'optimize',
            label: 'Optimization level',
            type: 'integer',
            control_type: 'number',
            optional: true,
            hint: 'A value from 0 to 3. Leave blank to use the API host default.'
          }
        ]
      end,

      execute: lambda do |_connection, input|
        optimize = input['optimize']
        unless optimize.nil? || (optimize.is_a?(Integer) && (0..3).cover?(optimize))
          error('Optimization level must be an integer from 0 to 3.')
        end

        language = input['language']
        unless language.nil? ||
               (language.is_a?(String) &&
                (language.empty? ||
                 (language.length <= 128 && language.match?(/\A[A-Za-z0-9_]+(?:\+[A-Za-z0-9_]+)*\z/))))
          error('Language must be up to 128 characters and contain language codes separated by +.')
        end
        language = nil if language == ''

        payload = {
          inputPath: input['inputPath'],
          outputPath: input['outputPath'],
          mode: input['mode'],
          createDestination: input['createDestination'],
          language: language,
          optimize: optimize
        }
        payload.delete_if { |_key, value| value.nil? }

        post('/api/v1/jobs', payload).after_response do |code, body, response_headers|
          acknowledgement = body.is_a?(Hash) ? body : {}
          {
            response_code: code,
            jobId: response_headers['x-job-id'] || acknowledgement['jobId'],
            status: acknowledgement['status'],
            statusUrl: acknowledgement['statusUrl'],
            resultArchive: body.is_a?(Hash) ? nil : body,
            contentDisposition: response_headers['content-disposition']
          }
        end
      end,

      output_fields: lambda do
        [
          { name: 'response_code', type: 'integer', label: 'HTTP response code' },
          { name: 'jobId', type: 'string', label: 'Job ID' },
          { name: 'status', type: 'string', label: 'Job status' },
          { name: 'statusUrl', type: 'string', label: 'Status URL' },
          {
            name: 'resultArchive',
            type: 'string',
            label: 'Result archive',
            hint: 'Populated only for synchronous submissions; the value is the returned tar.gz response body.'
          },
          { name: 'contentDisposition', type: 'string', label: 'Suggested archive filename' }
        ]
      end
    },

    submit_archive_job: {
      title: 'Submit an archive OCR job',
      description: 'Upload an input tar.gz archive and submit it for OCR processing.',

      input_fields: lambda do
        [
          {
            name: 'inputArchive',
            label: 'Input tar.gz archive',
            type: 'string',
            control_type: 'file',
            optional: true,
            hint: 'Upload a .tar.gz file. For Workato file contents, use the Compressed file contents field below.'
          },
          {
            name: 'inputArchiveContents',
            label: 'Compressed file contents',
            type: 'string',
            control_type: 'text',
            optional: true,
            hint: 'Map Compressed file contents from File tools by Workato. Configure compression as Gzip and archive format as Tar. The connector Base64-encodes these bytes; the API host must support inputArchiveContentsBase64.'
          },
          {
            name: 'mode',
            label: 'Execution mode',
            type: 'string',
            control_type: 'select',
            pick_list: [%w[Asynchronous asynchronous], %w[Synchronous synchronous]],
            default: 'asynchronous',
            optional: false
          },
          {
            name: 'language',
            label: 'OCR language(s)',
            type: 'string',
            optional: true,
            hint: 'Up to 128 characters; language codes separated by +, for example deu+eng. Leave blank to use the API host default.'
          },
          {
            name: 'optimize',
            label: 'Optimization level',
            type: 'integer',
            control_type: 'number',
            optional: true,
            hint: 'An integer from 0 to 3. Leave blank to use the API host default.'
          }
        ]
      end,

      execute: lambda do |_connection, input|
        optimize = input['optimize']
        unless optimize.nil? || (optimize.is_a?(Integer) && (0..3).cover?(optimize))
          error('Optimization level must be an integer from 0 to 3.')
        end

        language = input['language']
        unless language.nil? ||
               (language.is_a?(String) &&
                (language.empty? ||
                 (language.length <= 128 && language.match?(/\A[A-Za-z0-9_]+(?:\+[A-Za-z0-9_]+)*\z/))))
          error('Language must be up to 128 characters and contain language codes separated by +.')
        end
        language = nil if language == ''

        archive_contents = input['inputArchiveContents']
        archive_contents_base64 = Base64.strict_encode64(archive_contents) unless archive_contents.nil?

        payload = {
          inputArchive: input['inputArchive'],
          inputArchiveContentsBase64: archive_contents_base64,
          mode: input['mode'],
          language: language,
          optimize: optimize
        }
        payload.delete_if { |_key, value| value.nil? }

        post('/api/v1/jobs', payload)
          .request_format_multipart_form_data
          .after_response do |code, body, response_headers|
            acknowledgement = body.is_a?(Hash) ? body : {}
            {
              response_code: code,
              jobId: response_headers['x-job-id'] || acknowledgement['jobId'],
              status: acknowledgement['status'],
              statusUrl: acknowledgement['statusUrl'],
              resultArchive: body.is_a?(Hash) ? nil : body,
              contentDisposition: response_headers['content-disposition']
            }
          end
      end,

      output_fields: lambda do
        [
          { name: 'response_code', type: 'integer', label: 'HTTP response code' },
          { name: 'jobId', type: 'string', label: 'Job ID' },
          { name: 'status', type: 'string', label: 'Job status' },
          { name: 'statusUrl', type: 'string', label: 'Status URL' },
          {
            name: 'resultArchive',
            type: 'string',
            label: 'Result archive',
            hint: 'Populated only for synchronous submissions; the value is the returned tar.gz response body.'
          },
          { name: 'contentDisposition', type: 'string', label: 'Suggested archive filename' }
        ]
      end
    },

    get_job: {
      title: 'Get job status',
      description: 'Retrieve the current state and outcome of an OCR job.',

      input_fields: lambda do
        [{ name: 'jobId', label: 'Job ID', type: 'string', optional: false }]
      end,

      execute: lambda do |_connection, input|
        get("/api/v1/jobs/#{input['jobId']}")
      end,

      output_fields: lambda do |object_definitions|
        object_definitions['job_status']
      end
    },

    cancel_job: {
      title: 'Cancel an OCR job',
      description: 'Cancel a queued job or request cancellation of a running job. Poll the job until it reaches the cancelled state.',

      input_fields: lambda do
        [{ name: 'jobId', label: 'Job ID', type: 'string', optional: false }]
      end,

      execute: lambda do |_connection, input|
        post("/api/v1/jobs/#{input['jobId']}/cancel")
      end,

      output_fields: lambda do |object_definitions|
        object_definitions['job_status']
      end
    },

    resume_job: {
      title: 'Resume a failed or cancelled job',
      description: 'Requeue an existing failed or cancelled job using its saved input and OCR settings.',

      input_fields: lambda do
        [{ name: 'jobId', label: 'Job ID', type: 'string', optional: false }]
      end,

      execute: lambda do |_connection, input|
        post("/api/v1/jobs/#{input['jobId']}/resume")
      end,

      output_fields: lambda do
        [
          { name: 'jobId', type: 'string', label: 'Job ID' },
          { name: 'status', type: 'string', label: 'Job status' },
          { name: 'statusUrl', type: 'string', label: 'Status URL' }
        ]
      end
    },

    download_result: {
      title: 'Download processed files',
      description: "Download a succeeded job's output as a tar.gz archive.",

      input_fields: lambda do
        [{ name: 'jobId', label: 'Job ID', type: 'string', optional: false }]
      end,

      execute: lambda do |_connection, input|
        get("/api/v1/jobs/#{input['jobId']}/result")
          .response_format_raw
          .after_response do |_code, body, response_headers|
            {
              jobId: response_headers['x-job-id'],
              fileName: response_headers['content-disposition'],
              archive: body
            }
          end
      end,

      output_fields: lambda do
        [
          { name: 'jobId', type: 'string', label: 'Job ID' },
          { name: 'fileName', type: 'string', label: 'Content-Disposition' },
          {
            name: 'archive',
            type: 'string',
            label: 'Result archive',
            hint: 'The binary tar.gz response body.'
          }
        ]
      end
    },

    delete_job: {
      title: 'Delete a terminal job',
      description: 'Delete a job after it reaches a terminal state. Uploaded-job workspaces are removed; caller-managed directories are preserved.',

      input_fields: lambda do
        [{ name: 'jobId', label: 'Job ID', type: 'string', optional: false }]
      end,

      execute: lambda do |_connection, input|
        delete("/api/v1/jobs/#{input['jobId']}")
          .after_response do |code, _body, _response_headers|
            { deleted: code == 204 }
          end
      end,

      output_fields: lambda do
        [{ name: 'deleted', type: 'boolean', label: 'Deleted' }]
      end
    },

    cleanup_jobs: {
      title: 'Clean up terminal job data',
      description: 'Delete data for terminal jobs. The all scope also permanently removes the whole shared input and output directories of eligible jobs.',

      input_fields: lambda do
        [
          {
            name: 'scope',
            label: 'Cleanup scope',
            type: 'string',
            control_type: 'select',
            pick_list: [%w[Uploads uploads], %w[All all]],
            optional: false
          }
        ]
      end,

      execute: lambda do |_connection, input|
        post('/api/v1/jobs/cleanup', scope: input['scope'])
      end,

      output_fields: lambda do
        [
          { name: 'scope', type: 'string', label: 'Scope' },
          { name: 'jobsDeleted', type: 'integer', label: 'Jobs deleted' },
          { name: 'activeJobsSkipped', type: 'integer', label: 'Active jobs skipped' },
          { name: 'jobsSkippedForActiveData', type: 'integer', label: 'Jobs skipped for active data' },
          { name: 'jobsSkippedForServerData', type: 'integer', label: 'Jobs skipped for server data' },
          { name: 'jobsSkippedForUnsafePaths', type: 'integer', label: 'Jobs skipped for unsafe paths' }
        ]
      end
    }
  }
}