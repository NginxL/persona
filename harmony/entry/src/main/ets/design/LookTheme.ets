import { DeviceLook } from '../models/DeviceLook';
import { deviceLookAccentText } from '../core/DeviceLook';

/** Native tokens provide both color modes without maintaining a parallel system-mode observer. */
export function nativeLookTheme(look: DeviceLook, style: string): CustomTheme {
  const palette = look.palette(style);
  return {
    colors: { brand: palette.light.brand, fontEmphasize: palette.light.emphasize, iconEmphasize: palette.light.emphasize,
      compBackgroundEmphasize: palette.light.brand, fontOnPrimary: deviceLookAccentText(palette.light.brand), iconOnPrimary: deviceLookAccentText(palette.light.brand) },
    darkColors: { brand: palette.dark.brand, fontEmphasize: palette.dark.emphasize, iconEmphasize: palette.dark.emphasize,
      compBackgroundEmphasize: palette.dark.brand, fontOnPrimary: deviceLookAccentText(palette.dark.brand), iconOnPrimary: deviceLookAccentText(palette.dark.brand) }
  };
}

export function nativeMessageTheme(look: DeviceLook, style: string, own: boolean): CustomTheme {
  if (!own) return {};
  const palette = look.palette(style);
  return { colors: { compBackgroundPrimary: palette.light.userBubble, fontPrimary: palette.light.userBubbleText,
    compBackgroundSecondary: palette.light.userBubble, compBackgroundTertiary: palette.light.userBubble,
    fontSecondary: palette.light.userBubbleText, fontEmphasize: palette.light.userBubbleText },
    darkColors: { compBackgroundPrimary: palette.dark.userBubble, fontPrimary: palette.dark.userBubbleText,
      compBackgroundSecondary: palette.dark.userBubble, compBackgroundTertiary: palette.dark.userBubble,
      fontSecondary: palette.dark.userBubbleText, fontEmphasize: palette.dark.userBubbleText } };
}
