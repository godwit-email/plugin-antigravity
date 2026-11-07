#include <stdlib.h>
#include "sdk/godwit_plugin.h"

extern int antigravity_call(char *, uint8_t *, size_t, godwit_buffer *);

#if defined(__APPLE__) && defined(__x86_64__)
#include <pthread.h>

struct request {
    const char *method;
    const uint8_t *req;
    size_t len;
    godwit_buffer *out;
    int rc;
};

static void *run(void *ptr) {
    struct request *r = ptr;
    r->rc = antigravity_call((char *)r->method, (uint8_t *)r->req, r->len, r->out);
    return NULL;
}

/* Darwin amd64 Go runtimes share a TLS slot: never enter the plugin's Go
 * runtime on a host Go thread. A fresh C thread owns only the plugin runtime. */
static int call(const char *method, const uint8_t *req, size_t len, godwit_buffer *out) {
    struct request r = {method, req, len, out, -1};
    pthread_t thread;
    if (pthread_create(&thread, NULL, run, &r) != 0) return -1;
    if (pthread_join(thread, NULL) != 0) abort(); /* cannot return while r is in use */
    return r.rc;
}
#else
static int call(const char *method, const uint8_t *req, size_t len, godwit_buffer *out) {
    return antigravity_call((char *)method, (uint8_t *)req, len, out);
}
#endif

static void release(void *ptr, size_t len) {
    (void)len;
    free(ptr);
}

#if defined(_WIN32)
__declspec(dllexport)
#else
__attribute__((visibility("default"), used))
#endif
int godwit_plugin_init(uint32_t host_abi, godwit_plugin_api *api) {
    if (host_abi != GODWIT_PLUGIN_ABI || !api) return -1;
    api->abi_version = GODWIT_PLUGIN_ABI;
    api->call = call;
    api->free_buffer = release;
    return 0;
}
