/*
 * Godwit native plugin C ABI, version 1.
 *
 * A native plugin is a shared library (plugin.dylib, plugin.so or plugin.dll)
 * next to its plugin.json. Godwit checks the file's SHA-256 against what the
 * user (or the reviewed plugin directory) allowed, then loads it in a child
 * process, `godwit plugin-host`, one process per plugin. The library runs
 * with all of Godwit's rights: this is trusted code, not a sandbox.
 *
 * Requests and responses are JSON. Every response is an envelope:
 *   {"ok": true,  "result": {...}}
 *   {"ok": false, "error": {"code": "...", "message": "...", "retryable": false}}
 * Error codes: invalid_request, unsupported_language, rate_limited, upstream, login_required, internal.
 *
 * Methods (v1):
 *   describe   {}                                  -> {"name","version","abi","capabilities"}
 *              Must match plugin.json, or the plugin does not start.
 *   configure  {"config": {...},"dataDir":"/abs"}  -> {}
 *              After start and after each settings save; secrets included. dataDir
 *              is the plugin's own directory, kept across updates, gone on uninstall.
 *   translate  {"texts":[...],"source":"","target":"zh-Hans"} -> {"texts":[...],"source":"en"}
 *              source "" asks the plugin to detect it; tags are BCP 47. One text
 *              per paragraph of the mail, in order; they may be long, so split
 *              them as your service requires and answer the same number of texts.
 * With the capability "login" (the user signs in to the plugin's service; the
 * plugin keeps the credentials, in the system's keychain where it can):
 *   login.status {}                                -> {"signedIn":false,"account":"","notice":{...}}
 *              notice (a label: text, or {"default":...,"<lang>":...}) is what the user
 *              must read before signing in. Answer fast: Godwit asks when it lists plugins.
 *   login.start  {}                                -> {"flow":"id"}
 *              Return at once, with the slow work (a download, the sign-in) under way.
 *   login.poll   {"flow":"id"}                     -> {"state":"preparing"|"open"|"done"|"failed",
 *                                                      "url":"https://...","account":"","message":""}
 *              Godwit asks about once a second and opens url, in the state "open", once.
 *   login.cancel {"flow":"id"}                     -> {}
 *   login.logout {}                                -> {}
 *   models.list answers the error code login_required while signed out.
 * Unknown methods answer invalid_request.
 */
#ifndef GODWIT_PLUGIN_H
#define GODWIT_PLUGIN_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" { /* the host looks the init symbol up by its C name */
#endif

#define GODWIT_PLUGIN_ABI 1

/* Puts godwit_plugin_init in the library's export table: MSVC exports nothing
 * by default, and GCC or Clang with -fvisibility=hidden would hide it. */
#if defined(_WIN32)
#define GODWIT_PLUGIN_EXPORT __declspec(dllexport)
#else
#define GODWIT_PLUGIN_EXPORT __attribute__((visibility("default")))
#endif

typedef struct {
    void  *ptr;
    size_t len;
} godwit_buffer;

/*
 * method: NUL-terminated UTF-8. req: the request JSON, owned by the host and
 * valid only during the call (at most 1 MiB). out: a response envelope the
 * plugin allocates (at most 4 MiB); the host copies it and then calls
 * free_buffer on it. Return 0 when out holds an envelope (which may say
 * ok: false); non-zero for a failure below the envelope, out then unused.
 *
 * The host may call concurrently from several threads: call must be thread
 * safe. There is no cancellation; a call the host stops waiting for runs to
 * its end, and a call past the host's deadline (60 s) ends the process.
 * There is no shutdown entry: the host closes the process, so do not rely
 * on destructors. Neither side keeps a pointer it got from the other.
 * The process's stdout and stderr both go to Godwit's log; print freely.
 */
typedef int  (*godwit_call_fn)(const char *method, const uint8_t *req, size_t req_len, godwit_buffer *out);
typedef void (*godwit_free_fn)(void *ptr, size_t len);

typedef struct {
    uint32_t       abi_version; /* set to GODWIT_PLUGIN_ABI */
    godwit_call_fn call;
    godwit_free_fn free_buffer;
} godwit_plugin_api;

/*
 * The one exported symbol. host_abi is the version the host speaks; if it is
 * not GODWIT_PLUGIN_ABI return non-zero and leave api alone. Otherwise fill
 * api and return 0.
 */
#ifndef GODWIT_PLUGIN_NO_INIT_DECL
GODWIT_PLUGIN_EXPORT int godwit_plugin_init(uint32_t host_abi, godwit_plugin_api *api);
#endif

#ifdef __cplusplus
}
#endif

#endif
