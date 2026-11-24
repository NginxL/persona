package persona.feature.inbox

import persona.core.contracts.api.DeliveryState
import persona.core.designsystem.PendingStatus
import persona.core.inbox.*

internal fun DeliveryState.pendingStatus(): PendingStatus? = when (this) {
    DeliveryState.Queued, DeliveryState.WaitingForNetwork -> PendingStatus.Waiting
    DeliveryState.Sending -> PendingStatus.Sending
    DeliveryState.Unknown -> PendingStatus.Unconfirmed
    DeliveryState.Verifying -> PendingStatus.Verifying
    DeliveryState.NeedsConfirmation -> PendingStatus.NeedsReview
    DeliveryState.Refused, DeliveryState.Rejected -> PendingStatus.Refused
    DeliveryState.Sent, DeliveryState.Held -> null
}

internal fun ReplyDelivery.pendingStatus(): PendingStatus? = when (this) {
    ReplyDelivery.Queued, ReplyDelivery.WaitingForNetwork -> PendingStatus.Waiting
    ReplyDelivery.Sending -> PendingStatus.Sending
    ReplyDelivery.Unknown -> PendingStatus.Unconfirmed
    ReplyDelivery.Verifying -> PendingStatus.Verifying
    ReplyDelivery.NeedsConfirmation -> PendingStatus.NeedsReview
    ReplyDelivery.Refused -> PendingStatus.Refused
    ReplyDelivery.Ready, ReplyDelivery.Sent -> null
}
