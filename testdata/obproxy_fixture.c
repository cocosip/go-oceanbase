/* Independent fixture for the published C ABI; no proxy or database is started. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#ifdef _WIN32
#include <windows.h>
#define EXPORT __declspec(dllexport)
#else
#include <unistd.h>
#define EXPORT __attribute__((visibility("default")))
#endif

static int mode(void) {
  const char *value = getenv("OB_TEST_VC_MODE");
  return value ? atoi(value) : 0;
}
static void record(const char *value) {
  const char *path = getenv("OB_TEST_VC_RECORD");
  FILE *file = path ? fopen(path, "ab") : NULL;
  if (!file) abort();
  fprintf(file, "%s\n", value);
  fclose(file);
}
EXPORT int ob_proxy_vc_c_init(const char *config, void **handle) {
  record(config);
  /* Vendor output must not corrupt the readiness pipe. */
  puts("native stdout noise"); fflush(stdout);
  fputs("native stderr noise\n", stderr); fflush(stderr);
  if (mode() == 5) {
    for (;;) {
#ifdef _WIN32
      Sleep(INFINITE);
#else
      pause();
#endif
    }
  }
  if (mode() == 1) return -4000;
  if (mode() == 2) return 0;
  *handle = malloc(1);
  if (!*handle) return -4013;
  return mode() == 3 ? -4000 : 0;
}
#ifndef OB_TEST_MISSING_CLOSE
EXPORT int ob_proxy_vc_c_close(void *handle) {
  if (!handle) return -4002;
  free(handle);
  record("close");
  return mode() == 4 ? -4201 : 0;
}
#endif
