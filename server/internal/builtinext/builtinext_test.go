package builtinext

import (
	"context"
	"testing"
)

func TestProgramsAndVolc(t *testing.T) {
	t.Cleanup(func() { programs, volc, voice = map[string]program{}, nil, nil })
	if _, ok := LookupProgram("x"); ok {
		t.Fatal("an unknown program was found")
	}
	if _, ok := VolcSpeech(); ok {
		t.Fatal("the Volcengine extension is there without its module")
	}
	if ProgramUsage("personad") != "" {
		t.Fatal("usage lines without a program")
	}
	AddProgram("x", "the x extension", func(context.Context, string) error { return nil })
	if got, want := ProgramUsage("personad"), "  personad x                the x extension\n"; got != want {
		t.Fatalf("usage %q, want %q", got, want)
	}
	if run, ok := LookupProgram("x"); !ok || run(context.Background(), "") != nil {
		t.Fatal("the program provided is not found")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a program provided twice did not panic")
			}
		}()
		AddProgram("x", "", nil)
	}()
	ProvideVolc(Volc{ExtensionID: "volc", Voices: []string{"a"}})
	if v, ok := VolcSpeech(); !ok || v.ExtensionID != "volc" || v.Voices[0] != "a" {
		t.Fatalf("%+v %v", v, ok)
	}
	if _, ok := VoiceExtension(); ok {
		t.Fatal("the voice extension is there without its module")
	}
	ProvideVoice(Voice{TranscriptionContribution: "gpt-live"})
	if v, ok := VoiceExtension(); !ok || v.TranscriptionContribution != "gpt-live" {
		t.Fatalf("%+v %v", v, ok)
	}
}
