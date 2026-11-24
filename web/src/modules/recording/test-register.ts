// Tests only: registers this module once, as the module list does on the page, so a recording's knowledge page
// (views/knowledge/KBDetail.tsx) draws its parts through the `kb.detail` slot.
import { registerModule, registeredModules } from "../../core/module-host";
import { loadKBDetailParts } from "../../core/ModuleKBDetail";
import { recording } from "./module";

if (!registeredModules().some((m) => m.id === recording.id)) registerModule(recording);
// its code in before a test draws, as it is once the page has opened one recording
await loadKBDetailParts("recordings");
