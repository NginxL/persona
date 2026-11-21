<!-- English title and body: a squash merge makes them the commit on main. Link the issue with a closing keyword, for example "Fixes #123" (or "Refs #123" to link without closing). -->

## What changed and why



## Verification

<!-- Commands you ran and their results; screenshots for UI changes (light and dark), shown here and not committed. Say what was not run. -->



## Apple performance

<!-- Required when the change touches apple/; otherwise write "Not applicable".
     Paste the review checklist from docs/apple/performance.md and mark each rule done, not applicable, or deviated (with the reason).
     A performance-sensitive change (chat list, composer, Agents list, pushes, launch, images) adds before / after numbers:
     device or simulator, build settings, runs, range. -->



## Design principles self-check

<!-- Required when the change touches UI, settings or defaults, or how a number or status is counted; otherwise write "Not applicable" and delete the list.
     Mark each item done (with its evidence), not applicable, or deviated (with the reason). Rules and how to check: docs/design/PRINCIPLES.md.
     UI changes also paste the "UI change self-check" from docs/design/DESIGN_SYSTEM.md. -->

- [ ] 1 Defaults first: the default in one plain sentence; works on a fresh install with untouched settings.
- [ ] 2 Progressive disclosure: advanced options off by default, in Settings or an advanced group; primary screens unchanged with the feature off.
- [ ] 3 User control: each new setting is visible, changeable and reversible; an Agent changing it explains, confirms and offers rollback; new numbers say what they count.
- [ ] 4 Consistency first: components, patterns and count definitions reused are named; anything new has its reason in the docs.
- [ ] 5 Model and product separate: names which of speed, model, persona and features it touches; the others are unaffected.
- [ ] 6 Checkable: the issue's acceptance criteria are met, with evidence for each item above.
- [ ] 7 Fast, local-first and stateful: immediate local feedback, hub-confirmed Processing, recoverable failure; isolated before/after feedback and execution timings, with light/dark screenshots.
- [ ] Other platforms: done / issue #N / not applicable (reason)
- [ ] Parity list row updated
