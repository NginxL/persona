// The core's C entry points (docs/plans/plan-rust-core.md, "The boundary: a few entry points and bytes").
// Every message is a bincode-encoded wire type; the generated codecs read and write them.
#ifndef PERSONA_CORE_H
#define PERSONA_CORE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct PersonaCore PersonaCore;

// The client's callbacks. `changed` and `run` are called on the core's worker thread; `release` once, when the core
// is freed. `context` is passed back unchanged.
typedef struct PersonaCoreHost {
    void *context;
    void (*changed)(void *context, uint64_t version);
    void (*run)(void *context, const uint8_t *effect, size_t length);
    void (*release)(void *context);
} PersonaCoreHost;

// Bytes the core allocated; give them back with persona_core_bytes_free.
typedef struct PersonaCoreBytes {
    uint8_t *data;
    size_t length;
    size_t capacity;
} PersonaCoreBytes;

// Starts a core from an encoded Config. NULL when the config does not decode.
PersonaCore *persona_core_new(PersonaCoreHost host, const uint8_t *config, size_t length);
// Queues an encoded Action. Safe on the main thread: it only copies the bytes.
bool persona_core_dispatch(PersonaCore *core, const uint8_t *action, size_t length);
// The chat's encoded ChatPatch since the last drain. `chat` is UTF-8.
PersonaCoreBytes persona_core_drain(PersonaCore *core, const uint8_t *chat, size_t length);
void persona_core_bytes_free(PersonaCoreBytes bytes);
// Waits until queued actions are applied. For tests; never on a main thread.
bool persona_core_flush(PersonaCore *core, uint32_t timeout_ms);
// Stops the worker and frees the core; the host's release runs last.
void persona_core_free(PersonaCore *core);

#ifdef __cplusplus
}
#endif

#endif
