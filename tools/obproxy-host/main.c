/* OBProxy native host. Isolate the vendor's signals and allocator from the Go process. */
#include <errno.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>

#ifdef _WIN32
#include <windows.h>
#include <io.h>
#define write _write
#define read _read
#define close _close
#define dup _dup
#define dup2 _dup2
typedef HMODULE library_t;
static library_t load_library(const char *path) {
  wchar_t wide[32768];
  if (!MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, wide, 32768)) return NULL;
  return LoadLibraryW(wide);
}
static void *find_symbol(library_t library, const char *name) { return (void *)GetProcAddress(library, name); }
#define unload_library FreeLibrary
#else
#include <dlfcn.h>
#include <pthread.h>
typedef void *library_t;
static library_t load_library(const char *path) { return dlopen(path, RTLD_NOW | RTLD_LOCAL); }
#define find_symbol dlsym
#define unload_library dlclose
#endif

static int read_all(int fd, unsigned char *data, size_t size) {
  while (size) {
    int n = (int)read(fd, data, size);
    if (n < 0 && errno == EINTR) continue;
    if (n <= 0) return -1;
    data += n;
    size -= (size_t)n;
  }
  return 0;
}

static int write_all(int fd, const unsigned char *data, size_t size) {
  while (size) {
    int n = (int)write(fd, data, size);
    if (n < 0 && errno == EINTR) continue;
    if (n <= 0) return -1;
    data += n;
    size -= (size_t)n;
  }
  return 0;
}

#ifdef _WIN32
static DWORD WINAPI watch_parent(void *unused) {
#else
static void *watch_parent(void *unused) {
#endif
  unsigned char byte;
  (void)unused;
  /* No second configuration is accepted. EOF or extra bytes terminate all native threads. */
  while (read(STDIN_FILENO, &byte, 1) < 0 && errno == EINTR) {}
  _exit(0);
}

static int start_watcher(void) {
#ifdef _WIN32
  HANDLE thread = CreateThread(NULL, 0, watch_parent, NULL, 0, NULL);
  if (!thread) return -1;
  CloseHandle(thread);
#else
  pthread_t thread;
  if (pthread_create(&thread, NULL, watch_parent, NULL)) return -1;
  pthread_detach(thread);
#endif
  return 0;
}

static int encode_config(char *json, const unsigned char *input, uint32_t size) {
  static const char hex[] = "0123456789abcdef";
  char *out = json;
  if (!size) { memcpy(json, "{}", 3); return 0; }
  memcpy(out, "{\"proxy_config\":\"", 17);
  out += 17;
  for (uint32_t i = 0; i < size; ++i) {
    unsigned char c = input[i];
    if (!c) return -1;
    if (c == '"' || c == '\\') { *out++ = '\\'; *out++ = (char)c; }
    else if (c < 0x20) {
      memcpy(out, "\\u00", 4); out += 4;
      *out++ = hex[c >> 4]; *out++ = hex[c & 15];
    } else { *out++ = (char)c; }
  }
  *out++ = '"'; *out++ = '}'; *out = 0;
  return 0;
}

int main(int argc, char **argv) {
  unsigned char length[4], raw[4095];
  char config[4095 * 6 + 20];
  int control;
#ifdef _WIN32
  _setmode(STDIN_FILENO, _O_BINARY);
  _setmode(STDOUT_FILENO, _O_BINARY);
#endif
  if (argc != 2 || read_all(STDIN_FILENO, length, 4)) return 2;
  uint32_t size = (uint32_t)length[0] | (uint32_t)length[1] << 8 | (uint32_t)length[2] << 16 | (uint32_t)length[3] << 24;
  if (size > sizeof(raw) || read_all(STDIN_FILENO, raw, size) || encode_config(config, raw, size)) return 2;
  control = dup(STDOUT_FILENO);
  if (control < 0) return 2;
  /* Native printf/logging must never enter the readiness control stream. */
#ifdef _WIN32
  int discard = _open("NUL", _O_WRONLY);
#else
  int discard = open("/dev/null", O_WRONLY);
#endif
  if (discard < 0 || dup2(discard, STDOUT_FILENO) < 0 || dup2(discard, STDERR_FILENO) < 0) return 2;
  close(discard);
  if (start_watcher()) return 2;
  library_t library = load_library(argv[1]);
  int status = -1;
  if (library) {
    int (*initialize)(const char *, void **) = (int (*)(const char *, void **))find_symbol(library, "ob_proxy_vc_c_init");
    int (*release)(void *) = (int (*)(void *))find_symbol(library, "ob_proxy_vc_c_close");
    if (!initialize || !release) { unload_library(library); }
    else {
      void *client = NULL;
      status = initialize(config, &client);
      if (status == 0 && !client) status = -2;
      if (client) { int result = release(client); if (!status) status = result; }
      /* Keep the library loaded: native threads and atexit callbacks can survive init failure. */
    }
  }
  uint32_t code = (uint32_t)status;
  unsigned char ready[8] = {'O', 'B', 'V', '1', (unsigned char)code, (unsigned char)(code >> 8), (unsigned char)(code >> 16), (unsigned char)(code >> 24)};
  if (write_all(control, ready, sizeof(ready))) return 2;
  close(control);
  if (status) return 1;
  for (;;) {
#ifdef _WIN32
    Sleep(INFINITE);
#else
    pause();
#endif
  }
}
