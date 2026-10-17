#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#define API __declspec(dllexport)
#else
#define API __attribute__((visibility("default")))
#endif

typedef struct { size_t length; void *value; } buffer;
typedef struct { uint32_t length; void *elements; } oid;
typedef struct { size_t count; oid *elements; } oid_set;

static int mode;
static uint32_t live;
static const unsigned char user_oid[] = {42,134,72,134,247,18,1,2,1,1};
static const unsigned char krb_oid[] = {42,134,72,134,247,18,1,2,2};

API void gss_test_mode(int value) { mode = value; }
API uint32_t gss_test_live(void) { return live; }
static void *resource(void) { live++; return calloc(1,1); }
static void release(void **p) { if (*p) { live--; free(*p); *p = NULL; } }
static int kerberos(oid *m) { return m && m->length == sizeof krb_oid && memcmp(m->elements,krb_oid,sizeof krb_oid) == 0; }

API uint32_t gss_import_name(uint32_t *minor, buffer *input, oid *kind, void **out) {
    *minor = 0;
    if (input->length != 18 || memcmp(input->value,"service/db.example",18) || kind->length != sizeof user_oid || memcmp(kind->elements,user_oid,sizeof user_oid)) return 0x20000;
    *out = resource();
    return mode == 1 ? 0x10000 : 0;
}

API uint32_t gss_acquire_cred(uint32_t *minor, void *name, uint32_t lifetime, oid_set *mechs, int32_t usage, void **out, oid_set **actual, uint32_t *expires) {
    *minor = 0;
    if (name || lifetime || !mechs || mechs->count != 1 || !kerberos(mechs->elements) || usage != 1 || actual || expires) return 0x20000;
    *out = resource();
    return mode == 2 ? 0x10000 : 0;
}

API uint32_t gss_init_sec_context(uint32_t *minor, void *credential, void **ctx, void *name, oid *mech, uint32_t flags, uint32_t lifetime, void *bindings, buffer *input, oid **actual, buffer *output, uint32_t *returned, uint32_t *expires) {
    *minor = 0;
    if (!credential || !name || !kerberos(mech) || flags != 2 || lifetime || bindings || actual || expires) return 0x20000;
    *returned = mode == 4 ? 0 : 2;
    if (*ctx) {
        if (!input || input->length != 3 || memcmp(input->value,"\xff\x01\x02",3)) return 0x20000;
        return 0;
    }
    if (input) return 0x20000;
    *ctx = resource();
    output->length = 3;
    output->value = resource();
    /* Reallocate the tracked buffer without adding another live resource. */
    output->value = realloc(output->value,3);
    memcpy(output->value,"\x60\x00\xff",3);
    return mode == 3 ? 0x10000 : 1;
}

API uint32_t gss_release_buffer(uint32_t *minor, buffer *value) {
    *minor = 0; release(&value->value); value->length = 0;
    return mode == 5 ? 0x10000 : 0;
}
API uint32_t gss_delete_sec_context(uint32_t *minor, void **ctx, buffer *out) {
    *minor = 0;
    if (out) return 0x20000;
    release(ctx);
    return mode == 6 ? 0x10000 : 0;
}
API uint32_t gss_release_cred(uint32_t *minor, void **credential) {
    *minor = 0; release(credential);
    return mode == 6 ? 0x30000 : 0;
}
API uint32_t gss_release_name(uint32_t *minor, void **name) {
    *minor = 0; release(name);
    return mode == 6 ? 0x40000 : 0;
}
