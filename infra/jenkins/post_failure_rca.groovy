// Drop-in snippet for Jenkins pipeline `post.failure { ... }` blocks.
// Posts a signed webhook to bbctl-rca; the service does the RCA and posts to Slack.
//
// Prereqs in Jenkins:
//   - Credentials store: a "Secret text" credential with ID `bbctl-webhook-secret`
//     whose value matches the WEBHOOK_SECRET in AWS Secrets Manager.
//   - Pipeline must have `currentBuild.fullDisplayName`, `env.JOB_NAME`,
//     `env.BUILD_NUMBER` (Jenkins built-ins).
//   - SERVICE param: most stagger jobs already pass `params.SERVICE`. If absent,
//     falls back to JOB_NAME.
//
// Usage in your Jenkinsfile / vars/*.groovy:
//
//   post {
//     failure {
//       script { triggerRcaWebhook() }
//     }
//   }
//
// Or inline call: `triggerRcaWebhook()` inside a `script {}` block.

def call() {
    triggerRcaWebhook()
}

def triggerRcaWebhook() {
    def rcaUrl = env.BBCTL_RCA_URL ?: 'http://10.34.120.223:7070/v1/rca/webhook'
    def service = (params?.SERVICE ?: env.SERVICE ?: env.JOB_NAME) as String
    def payload = groovy.json.JsonOutput.toJson([
        job: env.JOB_NAME,
        build: env.BUILD_NUMBER.toInteger(),
        service: service,
    ])

    withCredentials([string(credentialsId: 'bbctl-webhook-secret', variable: 'WEBHOOK_SECRET')]) {
        // HMAC-SHA256 signature over the raw body
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
                timeout: 5,
                quiet: true,
                validResponseCodes: '100:599',
            )
            echo "[bbctl-rca] webhook status=${response.status} body=${response.content?.take(200)}"
        } catch (Exception e) {
            echo "[bbctl-rca] webhook error (non-fatal): ${e.message}"
        }
    }
}

@NonCPS
def hmacSha256(String secret, String body) {
    def mac = javax.crypto.Mac.getInstance('HmacSHA256')
    mac.init(new javax.crypto.spec.SecretKeySpec(secret.bytes, 'HmacSHA256'))
    return mac.doFinal(body.bytes).encodeHex().toString()
}

return this
