package persona.core.contracts.api

/** How externally sourced text reached a message send; ordinary typed messages and notification replies use null. */
object MessageVia {
    const val Share = "share"
    const val Intent = "intent"
    const val Card = "card"
}
