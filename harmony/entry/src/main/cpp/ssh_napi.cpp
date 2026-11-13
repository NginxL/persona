#include "napi/native_api.h"
#include "ssh_core.h"
#include <cmath>
#include <memory>
#include <string>
#include <utility>
#include <vector>

namespace {
void Wipe(std::vector<uint8_t>& bytes) {
  volatile uint8_t* cursor = bytes.data();
  for (size_t i = 0; i < bytes.size(); ++i) cursor[i] = 0;
  bytes.clear();
}
struct Work {
  napi_async_work work = nullptr;
  napi_deferred deferred = nullptr;
  bool closing = false;
  std::string id, host, user, pin, target;
  uint16_t port = 0, targetPort = 0;
  std::vector<std::string> resolvedHubIps;
  std::vector<const char*> resolvedHubIpPointers;
  std::vector<uint8_t> key, passphrase;
  char* result = nullptr;
  ~Work() { Wipe(key); Wipe(passphrase); persona_ssh_free(result); }
};
bool String(napi_env env, napi_value value, std::string& output, size_t limit) {
  napi_valuetype type; size_t size = 0;
  if (napi_typeof(env, value, &type) != napi_ok || type != napi_string ||
      napi_get_value_string_utf8(env, value, nullptr, 0, &size) != napi_ok || size > limit) return false;
  std::vector<char> buffer(size + 1, 0); size_t written = 0;
  if (napi_get_value_string_utf8(env, value, buffer.data(), buffer.size(), &written) != napi_ok || written != size) return false;
  output.assign(buffer.data(), written);
  return output.find('\0') == std::string::npos;
}
bool Property(napi_env env, napi_value object, const char* name, napi_value& output) {
  return napi_get_named_property(env, object, name, &output) == napi_ok;
}
bool Text(napi_env env, napi_value object, const char* name, std::string& output, size_t limit) {
  napi_value value; return Property(env, object, name, value) && String(env, value, output, limit);
}
bool TextArray(napi_env env, napi_value object, const char* name, std::vector<std::string>& output, uint32_t maxItems, size_t maxItemLength) {
  napi_value value; bool isArray = false; uint32_t length = 0;
  if (!Property(env, object, name, value) || napi_is_array(env, value, &isArray) != napi_ok || !isArray ||
      napi_get_array_length(env, value, &length) != napi_ok || length > maxItems) return false;
  output.reserve(length);
  for (uint32_t index = 0; index < length; ++index) {
    napi_value item;
    std::string text;
    if (napi_get_element(env, value, index, &item) != napi_ok || !String(env, item, text, maxItemLength)) return false;
    output.push_back(std::move(text));
  }
  return true;
}
bool Port(napi_env env, napi_value object, const char* name, uint16_t& output) {
  napi_value value; double number = 0;
  if (!Property(env, object, name, value) || napi_get_value_double(env, value, &number) != napi_ok ||
      !std::isfinite(number) || std::floor(number) != number || number < 1 || number > 65535) return false;
  output = static_cast<uint16_t>(number); return true;
}
bool Bytes(napi_env env, napi_value object, const char* name, std::vector<uint8_t>& output, size_t limit) {
  napi_value value, arrayBuffer; napi_typedarray_type type; size_t length = 0, offset = 0; void* data = nullptr;
  if (!Property(env, object, name, value) ||
      napi_get_typedarray_info(env, value, &type, &length, &data, &arrayBuffer, &offset) != napi_ok ||
      type != napi_uint8_array || length > limit || (length && !data)) return false;
  if (length) output.assign(static_cast<uint8_t*>(data), static_cast<uint8_t*>(data) + length);
  return true;
}
napi_value Error(napi_env env, const char* code) {
  napi_value text, error; napi_create_string_utf8(env, code, NAPI_AUTO_LENGTH, &text); napi_create_error(env, nullptr, text, &error); return error;
}
void Execute(napi_env, void* data) {
  auto* work = static_cast<Work*>(data);
  if (work->closing) { persona_ssh_close(work->id.c_str()); return; }
  work->resolvedHubIpPointers.reserve(work->resolvedHubIps.size());
  for (const auto& address : work->resolvedHubIps) work->resolvedHubIpPointers.push_back(address.c_str());
  PersonaSshConfig config { work->id.c_str(), work->host.c_str(), work->port, work->user.c_str(), work->pin.c_str(),
    work->target.c_str(), work->targetPort, work->resolvedHubIpPointers.data(), work->resolvedHubIpPointers.size(),
    work->key.data(), work->key.size(), work->passphrase.data(), work->passphrase.size() };
  work->result = persona_ssh_open(&config);
  Wipe(work->key); Wipe(work->passphrase);
}
void Complete(napi_env env, napi_status status, void* data) {
  std::unique_ptr<Work> work(static_cast<Work*>(data));
  if (status != napi_ok) {
    // Cancellation is immediate even if the open worker was never scheduled.
    persona_ssh_abandon(work->id.c_str());
    napi_reject_deferred(env, work->deferred, Error(env, "ssh_cancelled"));
  } else if (work->closing) {
    napi_value empty; napi_get_undefined(env, &empty); napi_resolve_deferred(env, work->deferred, empty);
  } else if (!work->result) {
    persona_ssh_abandon(work->id.c_str()); napi_reject_deferred(env, work->deferred, Error(env, "ssh_native_failed"));
  } else {
    // Rust emits bounded, sanitized JSON; do not return native SSH diagnostic strings.
    napi_value global, json, parse, text, result;
    bool valid = napi_get_global(env, &global) == napi_ok && napi_get_named_property(env, global, "JSON", &json) == napi_ok &&
      napi_get_named_property(env, json, "parse", &parse) == napi_ok &&
      napi_create_string_utf8(env, work->result, NAPI_AUTO_LENGTH, &text) == napi_ok &&
      napi_call_function(env, json, parse, 1, &text, &result) == napi_ok;
    if (!valid) {
      bool pending = false; napi_is_exception_pending(env, &pending);
      if (pending) { napi_value ignored; napi_get_and_clear_last_exception(env, &ignored); }
      persona_ssh_abandon(work->id.c_str()); napi_reject_deferred(env, work->deferred, Error(env, "ssh_native_failed"));
    } else {
      bool hasError = false; napi_has_named_property(env, result, "error", &hasError);
      if (hasError) {
        napi_value value; std::string code;
        if (!Property(env, result, "error", value) || !String(env, value, code, 64)) code = "ssh_native_failed";
        persona_ssh_abandon(work->id.c_str()); napi_reject_deferred(env, work->deferred, Error(env, code.c_str()));
      } else napi_resolve_deferred(env, work->deferred, result);
    }
  }
  napi_delete_async_work(env, work->work);
}
napi_value Queue(napi_env env, std::unique_ptr<Work> work) {
  napi_value promise, name;
  if (napi_create_promise(env, &work->deferred, &promise) != napi_ok) { persona_ssh_abandon(work->id.c_str()); return nullptr; }
  napi_create_string_utf8(env, "persona.ssh", NAPI_AUTO_LENGTH, &name);
  if (napi_create_async_work(env, nullptr, name, Execute, Complete, work.get(), &work->work) != napi_ok ||
      napi_queue_async_work(env, work->work) != napi_ok) {
    persona_ssh_abandon(work->id.c_str());
    if (work->work) napi_delete_async_work(env, work->work);
    napi_reject_deferred(env, work->deferred, Error(env, "ssh_native_failed"));
    return promise;
  }
  work.release(); return promise;
}
napi_value Open(napi_env env, napi_callback_info info) {
  size_t count = 1; napi_value args[1]; napi_get_cb_info(env, info, &count, args, nullptr, nullptr);
  auto work = std::make_unique<Work>();
  if (count != 1 || !Text(env, args[0], "id", work->id, 36) || !Text(env, args[0], "sshHost", work->host, 253) ||
      !Port(env, args[0], "sshPort", work->port) || !Text(env, args[0], "username", work->user, 128) ||
      !Text(env, args[0], "hostKey", work->pin, 50) || !Text(env, args[0], "targetHost", work->target, 253) ||
      !Port(env, args[0], "targetPort", work->targetPort) || !TextArray(env, args[0], "resolvedHubIps", work->resolvedHubIps, 16, 45) ||
      !Bytes(env, args[0], "privateKey", work->key, 32768) ||
      !Bytes(env, args[0], "passphrase", work->passphrase, 4096) || work->key.empty()) {
    napi_throw_error(env, nullptr, "ssh_invalid_config"); return nullptr;
  }
  if (!persona_ssh_begin(work->id.c_str())) { napi_throw_error(env, nullptr, "ssh_busy"); return nullptr; }
  return Queue(env, std::move(work));
}
napi_value Close(napi_env env, napi_callback_info info) {
  size_t count = 1; napi_value args[1]; napi_get_cb_info(env, info, &count, args, nullptr, nullptr);
  auto work = std::make_unique<Work>(); work->closing = true;
  if (count != 1 || !String(env, args[0], work->id, 36)) { napi_throw_error(env, nullptr, "ssh_invalid_config"); return nullptr; }
  persona_ssh_cancel(work->id.c_str());
  return Queue(env, std::move(work));
}
napi_value IsOpen(napi_env env, napi_callback_info info) {
  size_t count = 1; napi_value args[1], value; napi_get_cb_info(env, info, &count, args, nullptr, nullptr); std::string id;
  bool open = count == 1 && String(env, args[0], id, 36) && persona_ssh_alive(id.c_str()); napi_get_boolean(env, open, &value); return value;
}
napi_value Init(napi_env env, napi_value exports) {
  napi_property_descriptor properties[] = {
    { "open", nullptr, Open, nullptr, nullptr, nullptr, napi_default, nullptr },
    { "close", nullptr, Close, nullptr, nullptr, nullptr, napi_default, nullptr },
    { "isOpen", nullptr, IsOpen, nullptr, nullptr, nullptr, napi_default, nullptr },
  };
  napi_define_properties(env, exports, sizeof(properties) / sizeof(properties[0]), properties); return exports;
}
}
static napi_module module = { 1, 0, nullptr, Init, "persona_ssh", nullptr, { 0 } };
extern "C" __attribute__((constructor)) void RegisterPersonaSsh() { napi_module_register(&module); }
