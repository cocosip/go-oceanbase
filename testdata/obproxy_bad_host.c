/* Corrupt/short readiness and immediate exit exercise Go pipe ownership. */
#include <stdint.h>
#include <string.h>
#include <unistd.h>
#ifdef _WIN32
#include <fcntl.h>
#include <io.h>
#define read _read
#define write _write
#endif
int main(int argc, char **argv) {
  unsigned char frame[4];
#ifdef _WIN32
  _setmode(0, _O_BINARY); _setmode(1, _O_BINARY);
#endif
  if (argc != 2 || read(0, frame, 4) != 4) return 2;
  uint32_t size = (uint32_t)frame[0] | (uint32_t)frame[1] << 8 | (uint32_t)frame[2] << 16 | (uint32_t)frame[3] << 24;
  for (uint32_t i = 0; i < size; ++i) if (read(0, frame, 1) != 1) return 2;
  const unsigned char ready[8] = {'O', 'B', 'V', '1', 0x60, 0xf0, 0xff, 0xff};
  if (!strcmp(argv[1], "status")) return write(1, ready, 8) != 8;
  if (!strcmp(argv[1], "short")) return write(1, ready, 4) != 4;
  return write(1, "BAD!\0\0\0\0", 8) != 8;
}
