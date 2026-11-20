// What Antigravity says to the user for any failed run; the cause is in shortError / modelErrorMessage.
const GENERIC_USER_ERROR = /^agent execution terminated due to error\.?$/i

// Native JSONL exports can omit error steps that remain in the conversation service.
export function nativeErrorStep(step, index) {
  if (step?.type !== 'CORTEX_STEP_TYPE_ERROR_MESSAGE') return null
  if (['CORTEX_STEP_STATUS_CANCELLED', 'CORTEX_STEP_STATUS_CANCELED'].includes(step.status)) return null
  const error = step.errorMessage?.error
  if (!error) return null
  const specific = [error.shortError, error.modelErrorMessage].find((t) => typeof t === 'string' && t.trim())
  let text = error.userErrorMessage || error.modelErrorMessage || error.shortError
  if (typeof text === 'string' && GENERIC_USER_ERROR.test(text.trim()) && specific) text = specific
  if (typeof text !== 'string' || !text.trim()) return null
  const info = { turn_error: text.trim() }
  let details
  try { details = typeof error.details === 'string' ? JSON.parse(error.details) : error.details } catch {}
  const entries = Array.isArray(details?.error?.details) ? details.error.details : []
  const quota = entries.find((entry) => entry?.reason === 'QUOTA_EXHAUSTED')
  const reset = quota?.metadata?.quotaResetTimeStamp
  if (typeof reset === 'string' && Number.isFinite(Date.parse(reset))) {
    info.turn_error_kind = 'usage_limit'
    info.turn_error_resets_at = new Date(reset).toISOString()
  }
  const code = Number(error.errorCode)
  if (code === 429) info.turn_error_code = 'rate_limited'
  // The service refused the request as it is (an image it cannot read, an invalid argument, the model): the same
  // conversation sends the same request again, so a Continue fails the same way. 401 may be transient (runtime.js
  // retries it once), 408 and 429 are worth waiting for.
  if (code >= 400 && code < 500 && ![401, 408, 429].includes(code) && !info.turn_error_kind) {
    info.turn_error_kind = 'user'
    const reason = entries.find((entry) => typeof entry?.reason === 'string' && entry.reason)?.reason
    if (reason && !info.turn_error.includes(reason)) info.turn_error += ` (${reason})`
  }
  return {
    step_index: index,
    type: 'ERROR_MESSAGE',
    status: 'DONE',
    created_at: step.metadata?.createdAt,
    content: info.turn_error,
    turn_error: info,
  }
}
