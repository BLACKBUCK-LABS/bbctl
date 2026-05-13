// Drop-in shared lib for Jenkins post.failure blocks.
// On pipeline failure: POST signed webhook to bbctl-rca, get JSON RCA back,
// pretty-print it to console + set build description for at-a-glance triage.
//
// Prereqs in Jenkins:
//   - "Secret text" credential with ID `bbctl-webhook-secret` matching the
//     WEBHOOK_SECRET in AWS Secrets Manager.
//   - HTTP Request plugin installed.
//
// Usage in vars/<pipeline>.groovy:
//
//   post {
//     failure {
//       script { triggerRcaWebhook() }
//     }
//   }

def call() {
    return triggerRcaWebhook()
}

// Returns parsed RCA Map on success, null on failure. Always renders to
// console + sets build description.
def triggerRcaWebhook() {
    def rcaUrl = env.BBCTL_RCA_URL ?: 'https://bbctl.blackbuck.com/rca/v1/rca/webhook'
    def service = (params?.SERVICE ?: env.SERVICE ?: env.JOB_NAME) as String
    def payload = groovy.json.JsonOutput.toJson([
        job: env.JOB_NAME,
        build: env.BUILD_NUMBER.toInteger(),
        service: service,
    ])

    withCredentials([string(credentialsId: 'bbctl-webhook-secret', variable: 'WEBHOOK_SECRET')]) {
        def sig = 'sha256=' + hmacSha256(WEBHOOK_SECRET, payload)
        try {
            def response = httpRequest(
                url: rcaUrl,
                httpMode: 'POST',
                contentType: 'APPLICATION_JSON',
                requestBody: payload,
                customHeaders: [
                    [name: 'X-Bbctl-Signature', value: sig, maskValue: true],
                ],
                timeout: 60,           // RCA takes 10-30s with LLM call
                quiet: true,
                validResponseCodes: '100:599',
            )
            if (response.status == 200) {
                def rca = readJSON text: response.content
                renderRca(rca)
                return rca
            } else {
                echo "[bbctl-rca] HTTP ${response.status}: ${response.content?.take(300)}"
                return null
            }
        } catch (Exception e) {
            echo "[bbctl-rca] webhook error (non-fatal): ${e.message}"
            return null
        }
    }
}

// Build a one-paragraph human-friendly summary suitable for VictorOps/Slack
// message field. Pulls Finding + Action from suggested_fix.
def buildAlertMessage(Map rca) {
    if (!rca) return ''
    def lines = []
    lines << "🤖 *Auto-RCA* (class: ${rca.error_class ?: '?'}, stage: ${rca.failed_stage ?: '?'}, conf: ${rca.confidence ?: '?'})"
    lines << "Summary: ${rca.summary ?: '—'}"
    def fix = rca.suggested_fix
    if (fix instanceof Map) {
        def finding = fix.Finding ?: fix.finding
        def action = fix.Action ?: fix.action
        if (finding) lines << "Finding: ${finding}"
        if (action)  lines << "Action: ${(action as String).take(400)}"
    }
    if (rca.request_id) lines << "request_id: ${rca.request_id}"
    return lines.join('\n')
}

def renderRca(Map rca) {
    def cls = rca.error_class ?: 'unknown'
    def stage = rca.failed_stage ?: '—'
    def summary = rca.summary ?: '—'
    def conf = rca.confidence ?: '—'
    def reqId = rca.request_id ?: '—'

    // Build the formatted console block
    def lines = []
    lines << ''
    lines << '╔══════════════════════════════════════════════════════════════════╗'
    lines << '║                      bbctl-rca — Auto RCA                        ║'
    lines << '╚══════════════════════════════════════════════════════════════════╝'
    lines << "  class:       ${cls}"
    lines << "  failed_stage:${stage}"
    lines << "  confidence:  ${conf}"
    lines << ''
    lines << "  Summary:"
    lines << "    ${summary}"
    lines << ''
    lines << "  Root cause:"
    wrap(rca.root_cause ?: '—', 70).each { lines << "    ${it}" }
    lines << ''
    lines << "  Suggested fix:"
    def fix = rca.suggested_fix
    if (fix instanceof Map) {
        fix.each { k, v ->
            lines << "    [${k}]"
            wrap((v as String), 70).each { lines << "      ${it}" }
        }
    } else {
        wrap((fix as String), 70).each { lines << "    ${it}" }
    }
    lines << ''
    def cmds = rca.suggested_commands ?: []
    if (cmds) {
        lines << "  Commands:"
        cmds.eachWithIndex { c, i ->
            lines << "    ${i + 1}. [${c.tier ?: '?'}] ${c.cmd ?: ''}"
            if (c.rationale) lines << "       → ${c.rationale}"
        }
        lines << ''
    }
    def ev = rca.evidence ?: []
    if (ev) {
        lines << "  Evidence:"
        ev.take(5).each { e ->
            def verified = e.verified == true ? '✓' : (e.verified == false ? '✗' : '?')
            lines << "    [${verified}] ${e.source}: ${(e.snippet ?: '').take(120)}"
        }
        lines << ''
    }
    lines << "  request_id: ${reqId}"
    lines << "  full audit: /var/log/bbctl-rca/${reqId}.json on bbctl-ec2"
    lines << ''

    echo lines.join('\n')

    // Also set a short description so the build list page shows it at a glance
    def descLines = []
    descLines << "<b>RCA:</b> ${escapeHtml(summary)}"
    descLines << "<b>class:</b> ${cls} | <b>stage:</b> ${stage} | <b>conf:</b> ${conf}"
    if (fix instanceof Map && fix.Finding) {
        descLines << "<b>Finding:</b> ${escapeHtml((fix.Finding as String).take(200))}"
    }
    currentBuild.description = descLines.join('<br/>')
}

def wrap(String text, int width) {
    if (!text) return ['—']
    def out = []
    def cur = ''
    text.split(/\s+/).each { word ->
        if ((cur + ' ' + word).length() > width) {
            if (cur) out << cur
            cur = word
        } else {
            cur = cur ? "${cur} ${word}" : word
        }
    }
    if (cur) out << cur
    return out
}

@NonCPS
def escapeHtml(String s) {
    return s?.replace('&', '&amp;')?.replace('<', '&lt;')?.replace('>', '&gt;') ?: ''
}

@NonCPS
def hmacSha256(String secret, String body) {
    def mac = javax.crypto.Mac.getInstance('HmacSHA256')
    mac.init(new javax.crypto.spec.SecretKeySpec(secret.bytes, 'HmacSHA256'))
    return mac.doFinal(body.bytes).encodeHex().toString()
}

return this
