# HarmonyOS client lessons

## Native controls need actual device evidence

API 26 Select can retain a selected index without displaying its text when value is omitted. Bind both selection
and visible value; a compiled chooser with a blank capsule does not explain its purpose. Inspect restored screens
as well as a page opened after connection. A resource model can otherwise bind before the first authenticated
repository exists and remain offline after reconnection. Rebind reads to the captured owner without replaying writes
or replacing the user's editor state.

Native floating Tabs own their system inset. Their bottom margin is additional design spacing, not another copy of
the gesture area. Verify the title bar, drag handle and bottom tabs from the actual Navigation tree, including a
compact cover window and an expanded foldable that still falls in the MD class.

## Picker pauses are lifecycle transitions

Photo and document pickers can hide the destination and background its Ability. Closing the attachment controller
on every hidden callback invalidates a valid picker result. Preserve only an in-progress picker belonging to the
same immutable Hub/manager/machine/session, and recheck that target when the result returns. Reconnection can change
the transport generation without changing the selected local draft. A same-destination route restore must not
unnecessarily unmount and recreate its content.

## Acknowledgements and attachment cleanup are separate facts

Do not persist photo Base64 in the operation journal: native RDB cursor rows have a practical readable limit, even
when the HTTP request is accepted. Index bounded outbox rows separately and let selected sandbox files own payloads.
An exact client-message echo can prove delivery before an HTTP timeout or after a page was closed. Its draft revision
and selected attachment revisions need central cleanup, independent of the original view's lifetime.

Suppress acknowledged selections before fallible cleanup I/O. Keep a durable cleanup obligation until success and
exclude it from ordinary terminal-history pruning. A late receipt must not downgrade to unknown or make the same
operation retryable. Scope checks alone do not fence Forget followed immediately by re-pairing with the same ID:
capture storage epochs before queuing read/modify/write work and propagate them through the actual write.

## Keep test resources separate

A native TLS fixture that rotates its final certificate needs a fresh fixture before the next run. It must never
rotate a paired Hub's certificate. SDK test HAPs stop the normal Ability; reopen the application before taking UI
evidence. With several emulators running, use the device inventory and observed dimensions rather than assuming an
emulator table's serial column still names the device expected by the test.

Use an owned application directory for local Codex/Agent validation. Removing its node-session-directory override
can cause the node to scan normal personal Agent histories. Keep the override through service rebuilds and restarts.

## System icon resources may be transformed

The approved source layers are 1024-square. API 26 may encode packaged layers as 512-square RGBA even when the source
background is RGB and fully opaque. Validate reference IDs, content and alpha rather than treating the packaged
encoding as a source-art requirement. AppScope and the entry module must reference the same approved brand.

## Bind native menu builders through ArkUI syntax

A successful ArkTS build does not prove a context menu can open. Pass a parameterized local Builder as
`bindContextMenu(this.rowMenu(row), ResponseType.LongPress)`; use the method reference for a parameterless Builder.
On API 26, wrapping the invocation in an ordinary arrow function or manually calling `bind` leaves a bare function
in emitted code, while the supported Builder syntax produces the native Builder object. Inspect generated code
when diagnosing this boundary, then long-press a real row and invoke an action on the running application.

## Foldability does not guarantee crease geometry

A HarmonyOS 7 emulator can report a half-folded display while returning no current crease region. The native
FoldSplitContainer then fails inside its own crease lookup when entering landscape hover; an independent minimal
native example reproduces it. Sample valid geometry when creating the conversation and retain its native content
identity through configuration changes. Without geometry, keep ordinary native Column/Scroll layout and record
hover/hinge avoidance as unverified on that image. Do not invent a crease or fabricate a hover screenshot.

## Retained builders need current data

A stable `ForEach` key retains its item generator and the references it captured. Replacing an immutable row alone does not update that captured object. Keep a renderer-owned observed wrapper, update its traced value, and dereference it inside native attributes and deferred actions. Give replacement wrappers different native keys when their owner or scope changes. Use map indexes so list reconciliation remains linear.

For JSON extension pages, retain an observed presentation copy and reconcile matching nodes in place. Primitive read-only leaves need content-sensitive keys; action payloads and changing builder arguments need native bindings. Include page/model/epoch generation at the presentation boundary, and read current section positions for positional protocol keys. Actual API 26 checks exposed stale nested chart values with iterator-derived rows; explicit indexed reads updated those values while preserving the focused field and draft. Portable tests alone did not reveal this native dependency behavior.

## Freeze membership after initialization

A split detail can become active while the Agents workspace is awaiting its saved view. Global directory readiness does not mean that workspace has loaded its initial rows: freezing membership at that point retained an empty list until the user changed a filter. Gate the first freeze on workspace initialization, then rebuild when opening completes. Keep the existing freeze during later background refreshes so reading does not reorder the list.

## Complete the stream host gate

The native page's local confirmation and the Hub's saved-action permission gate are separate protocol steps for the same operation. After explicit consent, complete only the exact current pending page event's host interaction, with the current manager/session/epoch/version and structurally equal saved action. Reject expired, secret or input-bearing requests and recheck before dispatch. Do not route these native page sessions through generic Agent approval. Test a real component page as well as legacy render/action: a portable stream reducer test did not expose the missing host-interaction subscription.

## Invalidation retires presentation as well as authority

Disabling a navigation contribution must remove the old form, including loaded code/diff context. Clear that result cache and change the presentation generation so retained native controls cannot restore it under the same component key. Preserve ordinary drafts and captured write outcomes separately. Close known stream sessions before dropping their identifiers; detach pending opens so a late snapshot can be closed. A legacy page must also wait for a fresh render after reauthorization.

## Observe shared state at the rendering boundary

A shared transfer journal is not itself an ArkUI observed array. Read its published `@Trace` mirror before querying shared authoritative records, so the first addition registers a native dependency. Checking only later mutations of an already observed record misses the transition from no task to the first task. Verify that transition on a HAP as well as testing journal ownership and recovery in portable models.

## Pair emphasized fills with their foreground

Setting a dark-mode brand fill does not automatically choose readable native button text. Supply `fontOnPrimary` and `iconOnPrimary` together with `compBackgroundEmphasize`, using the foreground with sufficient contrast against the actual palette fill. Keep canvas emphasis and filled-control text as different roles. Check enabled controls in native sheets in both modes; disabled text is not evidence for the enabled foreground.

Use native Scroll content and a separate confirmation footer for a large-text transfer sheet. A List nested in that Scroll needs explicit boundary handoff. Let the sheet own safe-area avoidance rather than adding the root floating Tabs inset again. Inspect title menus at runtime too: a conditional expression mixing a CustomBuilder function and an item array can compile without displaying a menu. A native menu-item array can open a Menu bound to the NavDestination; a hidden-title pane binds the same native Menu actions to its More button. Verify the popup, scrolling and at least 48vp MenuItem targets at runtime.

A transmission setting is machine-wide, even when its sheet is transient. Share the in-flight write barrier with retained views, invalidate their old read authorization, and wait before reopening or reading settings. A sequence number alone can suppress a late write result while a faster GET has already revived an old enabled value. Preserve an acknowledged terminal transfer against a later failed status read.

A Builder argument containing an already-computed name or replaceable settings object can retain its original value. Keep the source/target branch stable and read the current traced settings and label inside native Text/Toggle attributes and callbacks. Verify both the list row and its named switch after a live rename. Shared-state admission must observe its published mirror before a plain Map gate can return early; otherwise clearing an in-flight write may leave the native Send button disabled even though a direct model call is ready.
