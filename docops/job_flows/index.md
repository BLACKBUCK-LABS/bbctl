# Job-flow index — pick the right doc per Jenkins job

This directory has one markdown file per Jenkins pipeline family. Each
file describes WHERE that family's code lives — main pipeline path,
top-level stages, which helper file each stage delegates to, and where
chains nest into other helpers.

These are FACTUAL descriptions derived from reading the actual pipeline
source. They contain only file path references — no error message
examples, no port numbers, no ARNs, no fix recipes. For fix recipes use
the error-class runbooks under `../runbooks/`.

## When to read a job_flow doc

After you have:
- the job name (from build_meta)
- the inline_script body (from get_jenkins_job_config) OR the failed
  stage marker from log_window

Call `list_job_flows()` to see the menu, match your job to one entry by
its `## Match` patterns, then call `read_job_flow(name)`.

If NO flow matches (new pipeline family), do not guess — read the main
pipeline script via repo_read_file and derive the chain by reading its
body.

## Where the source lives

| What | Where |
|------|-------|
| Main pipeline entrypoints | `jenkins_pipeline/<job_family>.groovy` |
| Pipeline-step helpers (Jenkins shared-lib convention) | `jenkins_pipeline/vars/<helperName>.groovy` |
| Shell scripts, config.json, canary configs, conf templates | `jenkins_pipeline/resources/...` |
| Utility classes | `jenkins_pipeline/src/com/blackbuck/...` |
| Terraform modules for infra creation (Prod & Prod+1) | `InfraComposer/...` |

## Universal Jenkins shared-lib facts (true for ALL flows)

- A call to `someName(...)` from any pipeline OR helper is the step
  defined in `vars/someName.groovy` (its `def call(...)` is the body).
- `libraryResource 'path/to/x'` resolves to `resources/path/to/x` on
  disk.
- `import com.blackbuck.<pkg>.<Class>` resolves to
  `src/com/blackbuck/<pkg>/<Class>.groovy`.

These are framework facts, not project-specific. They are TRUE for any
helper name you encounter — apply them to the helper names you read
out of the actual code, not to names you guessed.
