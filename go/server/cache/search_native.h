#ifndef TRUNK_SEARCH_NATIVE_H
#define TRUNK_SEARCH_NATIVE_H
#include <stdint.h>
#include <stddef.h>
typedef struct { const uint8_t *ptr; size_t len; } TTText;
typedef struct { const TTText *ptr; size_t len; } TTTexts;
typedef struct { const uint64_t *ptr; size_t len; } TTU64s;
typedef struct { const int64_t *ptr; size_t len; } TTI64s;
typedef struct { const double *ptr; size_t len; } TTF64s;
typedef struct { const uint8_t *ptr; size_t len; } TTBytes;
typedef struct { uint64_t k,state; TTTexts t,c,e; TTU64s u; TTI64s i; TTF64s f; TTBytes b; } TTCell;
typedef struct { const TTCell *ptr; size_t len; } TTCells;
typedef struct { TTText id; uint64_t source_row,row; TTCells cells; } TTDoc;
typedef struct { const TTDoc *ptr; size_t len; } TTDocs;
typedef struct { uint64_t k,code,has_code; TTText kind,op,value; } TTFilter;
typedef struct { const TTFilter *ptr; size_t len; } TTFilters;
typedef struct { uint64_t start,end; } TTRange;
typedef struct { const TTRange *ptr; size_t len; } TTRanges;
typedef struct { TTText op,path,binding,text,mode; uint64_t base,tail,first_ref; TTTexts fields,ids; TTDocs docs; TTRanges ranges; TTFilters filters; } TTRequest;
typedef struct { uint64_t tail; TTU64s rows; TTTexts ids; TTText error; } TTResponse;
typedef struct TTCancel TTCancel;
TTCancel *trunk_search_cancel_new(void);
void trunk_search_cancel_set(const TTCancel *);
void trunk_search_cancel_free(TTCancel *);
TTResponse *trunk_search_execute(const TTRequest *, const TTCancel *);
void trunk_search_result_free(TTResponse *);
#endif
