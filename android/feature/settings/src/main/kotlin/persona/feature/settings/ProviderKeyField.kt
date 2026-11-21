package persona.feature.settings

import android.content.res.Resources
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import persona.core.contracts.api.SecretReference
import persona.core.designsystem.JSwitchRow
import persona.core.designsystem.JTextField
import persona.core.designsystem.Tokens

/** "From provider · item", falling back to the item title or raw reference when its provider has gone. */
internal fun providerReferenceState(res: Resources, reference: SecretReference): String {
    if (reference.providerLabel.isBlank()) return reference.title?.takeIf { it.isNotBlank() } ?: reference.reference
    val from = res.getString(R.string.settings_ext_from_provider, reference.providerLabel)
    return reference.title?.takeIf { it.isNotBlank() }?.let { "$from · $it" } ?: from
}

/**
 * One provider key, with its own explicit value/reference mode. A stored reference stays visible even when new
 * references are unavailable; toggling modes always clears the typed input at the caller.
 */
@Composable
internal fun ProviderKeyField(
    value: String,
    onValueChange: (String) -> Unit,
    byReference: Boolean,
    onByReferenceChange: ((Boolean) -> Unit)?,
    stored: SecretReference?,
    label: String,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    supportingText: String? = null,
    placeholder: String? = null,
) {
    Column(modifier, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        if (onByReferenceChange != null) JSwitchRow(stringResource(R.string.settings_ext_use_reference), byReference,
            onByReferenceChange, enabled = enabled)
        JTextField(value, onValueChange, label = label, singleLine = true, enabled = enabled,
            placeholder = if (byReference) stored?.reference ?: stringResource(R.string.settings_ext_reference_placeholder) else placeholder,
            visualTransformation = if (byReference) VisualTransformation.None else PasswordVisualTransformation(),
            keyboardOptions = KeyboardOptions(keyboardType = if (byReference) KeyboardType.Uri else KeyboardType.Password),
            monospace = byReference, autofill = false,
            supportingText = listOfNotNull(
                stored?.let {
                    // A closed gate offers ordinary-key editing without hiding what is still stored on the hub.
                    providerReferenceState(androidx.compose.ui.platform.LocalResources.current, it) +
                        if (byReference) "" else "\n${it.reference}"
                },
                if (byReference) stringResource(R.string.settings_provider_reference_hint) else null,
                supportingText,
            ).joinToString("\n\n"))
    }
}
