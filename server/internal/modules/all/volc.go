//go:build !persona_without_volc && !persona_core_only

package all

import (
	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/modules/volc"
)

// The Volcengine speech module: its program (`personad volc`) and what the extension host shows of its extension.
func init() {
	modules = append(modules, volc.Module{})
	builtinext.AddProgram("volc", "the built-in Volcengine speech extension: the hub runs it (docs/extensions/volcengine-speech.md)", volc.Run)
	builtinext.ProvideVolc(builtinext.Volc{
		ExtensionID: volc.ExtensionID, TranscriptionContribution: volc.TranscriptionContribution,
		SpeechContribution: volc.SpeechContribution, StatusPage: volc.StatusPage, SecretKey: volc.SecretKey,
		Voices: volc.Voices, ASRModels: volc.ASRModels,
	})
}
