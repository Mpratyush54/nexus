/* C ABI for nexuscore (spec 7.3).
 * Windows: go build -tags nexuscorelib -buildmode=c-shared -o nexuscore.dll ./cmd/nexuscore
 * macOS:   go build -tags nexuscorelib -buildmode=c-archive -o libnexuscore.a ./cmd/nexuscore
 * The caller frees every pointer from nx_call with nx_free. Do not call nx_call
 * from inside the subscribe callback.
 */
#pragma once

typedef void (*nx_event_fn)(const char *json);

int nx_init(const char *config_json);
char *nx_call(const char *method, const char *request_json);
int nx_subscribe(nx_event_fn callback);
void nx_free(char *ptr);
int nx_shutdown(void);
