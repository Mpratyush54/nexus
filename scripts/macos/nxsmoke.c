/* Loads libnexuscore.dylib the way a desktop host (Uno/.NET, Swift) does:
 * dlopen at runtime, resolve the C ABI from bindings/nexuscore.h, then
 * nx_init -> nx_call -> nx_free -> nx_shutdown. Exits non-zero on failure.
 *
 *   cc -o nxsmoke scripts/macos/nxsmoke.c && ./nxsmoke ./libnexuscore.dylib
 */
#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>

typedef int (*nx_init_fn)(const char *);
typedef char *(*nx_call_fn)(const char *, const char *);
typedef void (*nx_free_fn)(char *);
typedef int (*nx_shutdown_fn)(void);

static void print_nofile(const char *when) {
    struct rlimit r;
    if (getrlimit(RLIMIT_NOFILE, &r) == 0)
        printf("RLIMIT_NOFILE %s: soft=%llu hard=%llu\n", when,
               (unsigned long long)r.rlim_cur, (unsigned long long)r.rlim_max);
}

static int call(nx_call_fn nx_call, nx_free_fn nx_free, const char *method,
                const char *args, const char *must_contain) {
    char *out = nx_call(method, args);
    if (!out) {
        fprintf(stderr, "FAIL %s: NULL result\n", method);
        return 1;
    }
    printf("%s -> %.600s%s\n", method, out, strlen(out) > 600 ? "..." : "");
    int bad = strncmp(out, "{\"ok\":true", 10) != 0 ||
              (must_contain && !strstr(out, must_contain));
    if (bad) fprintf(stderr, "FAIL %s (want ok:true%s%s)\n", method,
                     must_contain ? " and " : "", must_contain ? must_contain : "");
    nx_free(out);
    return bad;
}

int main(int argc, char **argv) {
    const char *lib = argc > 1 ? argv[1] : "./libnexuscore.dylib";
    print_nofile("before dlopen");
    void *h = dlopen(lib, RTLD_NOW | RTLD_LOCAL);
    if (!h) {
        fprintf(stderr, "FAIL dlopen %s: %s\n", lib, dlerror());
        return 2;
    }
    print_nofile("after dlopen (Go runtime initialised)");
    nx_init_fn nx_init = (nx_init_fn)dlsym(h, "nx_init");
    nx_call_fn nx_call = (nx_call_fn)dlsym(h, "nx_call");
    nx_free_fn nx_free = (nx_free_fn)dlsym(h, "nx_free");
    nx_shutdown_fn nx_shutdown = (nx_shutdown_fn)dlsym(h, "nx_shutdown");
    if (!nx_init || !nx_call || !nx_free || !nx_shutdown || !dlsym(h, "nx_subscribe")) {
        fprintf(stderr, "FAIL: missing C ABI symbol\n");
        return 3;
    }
    printf("PATH before nx_init: %s\n", getenv("PATH"));
    int rc = nx_init("{\"online\":false}");
    printf("nx_init -> %d\n", rc);
    printf("PATH after nx_init:  %s\n", getenv("PATH"));
    if (rc != 0) return 4;

    int failed = 0;
    failed |= call(nx_call, nx_free, "status.get", "{}", "\"online\":false");
    failed |= call(nx_call, nx_free, "agents.setup.browse", "{}", "\"agent\":\"claude\"");
    /* NXSMOKE_EXPECT_CLAUDE=1: a claude CLI exists only in ~/.local/bin and
     * PATH is launchd's bare default, as for an app opened from Finder. */
    const char *want_claude = getenv("NXSMOKE_EXPECT_CLAUDE");
    failed |= call(nx_call, nx_free, "harness.status", "{\"agent\":\"claude\"}",
                   want_claude && *want_claude == '1' ? "\"installed\":true" : NULL);

    char *bad = nx_call("no.such.method", "{}");
    printf("no.such.method -> %s\n", bad ? bad : "(null)");
    if (!bad || strncmp(bad, "{\"ok\":false", 11) != 0) {
        fprintf(stderr, "FAIL: unknown method must return ok:false\n");
        failed = 1;
    }
    nx_free(bad);

    printf("nx_shutdown -> %d\n", nx_shutdown());
    if (failed) return 5;
    printf("SMOKE OK\n");
    return 0;
}
