# Runbook: compliance

## What this class means
The Jenkins `JiraDetails` stage rejected the build before any deploy
action. The gate validates the build against Jira + GitHub. Five
distinct sub-modes exist; pick ONE based on the log + Jira ticket data.

## Detect signals
- `Compliance:` prefix in log lines
- `Jira ticket <KEY>` mentioned in failure message
- Failed stage = "Jira Details" (from `[Pipeline] { (Jira Details)` marker)
- `error_class` should be `compliance`

## Pipeline source to cross-check (MANDATORY)
- `jenkins_pipeline/vars/JiraDetails.groovy` — the gate implementation
- Whatever script_path Jenkins runs (e.g. `create-quick-infra.groovy`)
  for the call site

## Modes — pick one

### Mode 1 — Ticket status not in allowed list
**Log signal:** `Compliance: Jira ticket <KEY> is not READY FOR RELEASE
(current: Done)` or similar status mismatch.

**Drill plan:**
1. `repo_read_file("jenkins_pipeline", "vars/JiraDetails.groovy", 30, 80)`
2. `jira_get_ticket(<KEY>)` to confirm current status

**Action:**
```
Operator: open Jira ticket <KEY> in the Jira UI, transition status to
'READY FOR RELEASE' (or 'HOT FIX' for hotfix pipelines), then re-run.
```
Single path. No Option B.

### Mode 2 — Missing Signed Off Commit ID
**Log signal:** `Compliance: Jira ticket <KEY> has no Signed Off
commit id` or `Parent ticket has no Signed Off commit id`.

**Drill plan:**
1. `repo_read_file("jenkins_pipeline", "vars/JiraDetails.groovy", ...)`
2. `jira_get_ticket(<KEY>)` — confirm `custom_fields["Signed Off Commit ID"]`
   is null/missing

**Action:**
```
Operator: open Jira <KEY> → edit 'Signed Off Commit ID' field
(customfield_10973) → paste full 40-char SHA of COMMIT_ID =
<actual sha from log> → save. Re-run pipeline.
```
DO NOT suggest Jira REST API curl. UI edit only.

### Mode 3 — Commit SHA mismatch
**Log signal:** `Compliance: Signed Off commit id <X> does not match
resolved SHA <Y>`.

**Drill plan:**
1. `repo_read_file("jenkins_pipeline", "vars/JiraDetails.groovy", ...)`
2. `jira_get_ticket(<KEY>)` — get signed-off SHA
3. `github_get_commit(<repo>, <signed_off_sha>)` — see what was signed off
4. `github_get_commit(<repo>, <resolved_sha>)` — see what build resolved to
5. Compare authors / files_changed / dates

**Action (MUST output BOTH options):**
```
Option A (RECOMMENDED — operator passed wrong param):
  Re-run pipeline with COMMIT_ID/TAG = <signed_off_sha>.
  Check for: leading/trailing space in COMMIT_ID input,
  JFrog version string mistaken for git SHA, wrong branch/tag.

Option B (operator intends new commit — re-sign required):
  Update Jira <KEY> 'Signed Off Commit ID' to <resolved_sha>.
  Ensure merged PR title contains <KEY> (per Mode 5).
  Re-run pipeline.
```

### Mode 4 — Clone-of-clone chain
**Log signal:** `Compliance: clone-of-clone chain detected: X → Y → Z`.

**Drill plan:**
1. `jira_search('issuekey = X OR issuekey = Y OR issuekey = Z')`
2. For each ticket: `jira_get_ticket(<key>)` to see clone chain

**Action:**
```
Operator: identify the correct parent ticket (the original, not the
grandparent). Re-run pipeline with that ticket as Jira-Ticket param.
```

### Mode 5 — PR title missing Jira ticket ID
**Log signal:** `Compliance: merged PR title does not contain <KEY>`.

**Drill plan:**
1. `jira_get_ticket(<KEY>)` — confirm ticket exists
2. `github_find_pr_for_commit(<repo>, <sha>)` — get the offending PR

**Action:**
```
gh pr edit <PR_NUMBER> --title '<KEY> <existing title>'
```
Re-run pipeline.

## Output schema notes
- `error_class: "compliance"`
- `failed_stage: "Jira Details"`
- `evidence[]` must include:
  - `jenkins_log` line with the Compliance prefix
  - `jenkins_pipeline/vars/JiraDetails.groovy:<line>` (the check that fired)
  - `jira:<KEY>` (the ticket field that failed)
  - For Mode 3: BOTH `github:<repo>@<sha>` entries

## Common pitfalls
- DO NOT cite "clone detection" as cause unless log explicitly says so.
- DO NOT suggest `curl -X PUT ... atlassian.net/rest/api/2/issue/...` —
  custom-field edit via REST often needs special perms; UI is org-standard.
- DO NOT use BBCTL commands here — this is a Jira/GitHub fix, not an
  instance fix.
