// The voice module's descriptor (#13431, docs/web/web.md "Voice chat" and "Modules"): full-duplex voice chat with Persona
// or an Agent, from a chat's composer. Its code (chat/slots.tsx), its call's own chunk (chat/VoiceCall.tsx) and its
// strings (voice.json) load when a chat page first reaches its slots; none of it is on the first screen.
//
// Its hub flag is module.voice (core/module-host.ts, the hub flag rule): a hub that publishes module flags turns it off
// by leaving the flag out, every older hub keeps it on, and Settings › General › Optional features can turn it off
// in a browser. Off, the composer has no voice button and no call can start; typing and sending are unchanged.
import { t, byLanguage, type CatalogModule } from "../../i18n";
import { moduleFeature, type ModuleDescriptor } from "../../core/module-host";

export const voice: ModuleDescriptor = {
  id: "voice",
  feature: moduleFeature("voice"),
  title: () => t("settings-features.module_voice"),
  description: () => t("settings-features.module_voice_hint"),
  // the composer's button, the call's captions after the thread, its bar in the composer's place (and the row after a
  // call ended by itself), and "Asking by voice" on the card the call asks about
  chatSlots: ["composer.trailingAction", "chat.tail", "chat.bar", "interaction.asked"],
  i18nNamespaces: ["voice"],
  locales: byLanguage(import.meta.glob<CatalogModule>("../../i18n/locales/*/_meta.json", { query: "?catalog=module-voice" })),
  load: () => import("./chat/slots").then((m) => ({ chat: m.chat })),
};
