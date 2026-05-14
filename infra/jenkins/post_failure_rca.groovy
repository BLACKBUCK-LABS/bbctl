// Drop-in shared lib for Jenkins post.failure blocks.
// On pipeline failure: POST signed webhook to bbctl-rca, get JSON RCA back,
// pretty-print it to console + set build description for at-a-glance triage.
//
// Uses raw HttpURLConnection (no plugin dep) to match the existing VictorOps
// pattern. Only Jenkins steps used: withCredentials.
//
// Prereqs in Jenkins:
//   - "Secret text" credential with ID `bbctl-webhook-secret` matching the
//     WEBHOOK_SECRET in AWS Secrets Manager.
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
        def result
        try {
            result = postWebhook(rcaUrl, payload, sig)
        } catch (Exception e) {
            echo "[BB-AI] webhook transport error (non-fatal): ${e.message}"
            return null
        }
        if (result?.status == 200) {
            try {
                def rca = parseJson(result.body)
                renderRca(rca)
                return rca
            } catch (Exception e) {
                echo "[BB-AI] JSON parse error: ${e.message}"
                echo "[BB-AI] raw body: ${result.body?.take(500)}"
                return null
            }
        } else {
            echo "[BB-AI] HTTP ${result?.status}: ${result?.body?.take(300)}"
            return null
        }
    }
}

// Pure-Java HTTP POST. Returns [status: int, body: String]. Throws on
// transport error so caller logs once via echo (which can't run inside @NonCPS).
// @NonCPS keeps HttpURLConnection / streams off the persisted CPS heap.
@NonCPS
def postWebhook(String urlStr, String payload, String sig) {
    URL url = new URL(urlStr)
    HttpURLConnection conn = (HttpURLConnection) url.openConnection()
    conn.setRequestMethod('POST')
    conn.setDoOutput(true)
    conn.setRequestProperty('Content-Type', 'application/json')
    conn.setRequestProperty('X-Bbctl-Signature', sig)
    conn.setConnectTimeout(10_000)
    conn.setReadTimeout(60_000)   // LLM call: 10-30s typical

    OutputStream os = conn.getOutputStream()
    os.write(payload.getBytes('UTF-8'))
    os.close()

    int status = conn.getResponseCode()
    InputStream is = (status >= 200 && status < 400) ? conn.getInputStream() : conn.getErrorStream()
    String body = is != null ? is.getText('UTF-8') : ''
    conn.disconnect()
    return [status: status, body: body]
}

@NonCPS
def parseJson(String text) {
    return new groovy.json.JsonSlurper().parseText(text)
}

// Build a one-paragraph human-friendly summary suitable for VictorOps / Slack
// message field. Handles both shapes of suggested_fix (Map with
// Finding/Action/Verify keys; plain String for classes whose runbook uses a
// single-block format).
def buildAlertMessage(Map rca) {
    if (!rca) return ''
    def lines = []
    lines << "🤖 *BB-AI RCA* (class: ${rca.error_class ?: '?'}, stage: ${rca.failed_stage ?: '?'}, conf: ${rca.confidence ?: '?'})"
    lines << "Summary: ${rca.summary ?: '—'}"
    def fix = rca.suggested_fix
    if (fix instanceof Map) {
        def finding = fix.Finding ?: fix.finding
        def action = fix.Action ?: fix.action
        if (finding) lines << "Finding: ${finding}"
        if (action)  lines << "Action: ${(action as String).take(400)}"
    } else if (fix instanceof CharSequence) {
        // Single-block string — first ~400 chars give on-call enough to start
        // triage. Operator can hit Jenkins console for the full block.
        lines << "Fix: ${(fix as String).take(400)}"
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
    lines << '║               Jenkins Build RCA — Powered by BB-AI               ║'
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
    def reportUrl = rcaReportUrl(reqId)
    lines << "  request_id: ${reqId}"
    lines << "  📊 report:  ${reportUrl}"
    lines << ''

    echo lines.join('\n')

    // Also set a short HTML description so the build list page shows it at a
    // glance + a clickable link to the full HTML report.
    def descLines = []
    descLines << "<b>🤖 BB-AI RCA:</b> ${escapeHtml(summary)}"
    descLines << "<b>class:</b> ${cls} | <b>stage:</b> ${stage} | <b>conf:</b> ${conf}"
    if (fix instanceof Map && fix.Finding) {
        descLines << "<b>Finding:</b> ${escapeHtml((fix.Finding as String).take(200))}"
    }
    descLines << "<a href='${reportUrl}' target='_blank'>📊 Open full RCA report →</a>"
    currentBuild.description = descLines.join('<br/>')
}

// Canonical URL for the HTML report served by bbctl-rca. Override the host
// at runtime via BBCTL_RCA_REPORT_BASE_URL if testing against a different ALB
// / endpoint. The route `/rca/v1/report/<uuid>` is served by FastAPI.
def rcaReportUrl(String requestId) {
    def base = env.BBCTL_RCA_REPORT_BASE_URL ?: 'https://bbctl.blackbuck.com'
    return "${base}/rca/v1/report/${requestId}"
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
