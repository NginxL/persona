#pragma once
#include <cstddef>
#include <cstdint>
extern "C" {
struct PersonaSshConfig {
  const char* id;
  const char* ssh_host;
  uint16_t ssh_port;
  const char* user;
  const char* host_key;
  const char* target_host;
  uint16_t target_port;
  const char* const* resolved_hub_ips;
  size_t resolved_hub_ips_len;
  const uint8_t* private_key;
  size_t private_key_len;
  const uint8_t* passphrase;
  size_t passphrase_len;
};
bool persona_ssh_begin(const char* id);
char* persona_ssh_open(const PersonaSshConfig* config);
void persona_ssh_close(const char* id);
void persona_ssh_cancel(const char* id);
void persona_ssh_abandon(const char* id);
bool persona_ssh_alive(const char* id);
void persona_ssh_free(char* value);
}
