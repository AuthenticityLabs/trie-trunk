#ifndef TRUNK_NATIVE_LIVE_H
#define TRUNK_NATIVE_LIVE_H
#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
typedef struct { uint8_t id[16]; void *address; size_t length; uint64_t row; } TrunkLiveRecord;
typedef struct { uint8_t bytes[16]; } TrunkLiveId;
void *trunk_live_new(size_t);
void trunk_live_free(void *);
bool trunk_live_room(const void *, int64_t);
bool trunk_live_put(const void *, TrunkLiveRecord, const uint8_t *, uint64_t, bool, int64_t);
TrunkLiveRecord trunk_live_get(const void *, TrunkLiveId, bool, int64_t);
TrunkLiveRecord trunk_live_row(const void *, int64_t, uint64_t, int64_t);
bool trunk_live_revision(const void *, const uint8_t *, uint64_t);
void trunk_live_prune(const void *, int64_t);
void trunk_live_finish(const void *, int64_t);
size_t trunk_live_count(const void *, uint8_t);
typedef struct { const TrunkLiveRecord *data; size_t len; size_t capacity; } TrunkLiveResult;
TrunkLiveResult trunk_live_snapshot(const void *, const uint8_t *, uint8_t, int64_t, int64_t, int64_t);
void trunk_live_result_free(TrunkLiveRecord *, size_t, size_t);
int64_t trunk_live_first_pending(const void *);
#endif
