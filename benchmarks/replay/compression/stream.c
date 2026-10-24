// Native streaming codec benchmark. No production server changes.
#include <brotli/encode.h>
#include <brotli/decode.h>
#include <zlib.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#define CAP 65536
#define WARM 64
#define CHECK(x) do { if(!(x)){fprintf(stderr,"failed line %d: %s\n",__LINE__,#x);exit(1);} }while(0)
static uint64_t ns(void){struct timespec t;CHECK(!clock_gettime(CLOCK_MONOTONIC_RAW,&t));return (uint64_t)t.tv_sec*1000000000+t.tv_nsec;}
static int cmp(const void*a,const void*b){double x=*(const double*)a,y=*(const double*)b;return (x>y)-(x<y);}
static double pct(double*a,int n,int p){qsort(a,n,sizeof(double),cmp);return a[(n-1)*p/100];}
typedef struct{uint8_t*p;size_t n;}Packet;
typedef struct{int br;z_stream enc,dec;BrotliEncoderState*be;BrotliDecoderState*bd;}Codec;
static void init(Codec*c,int br,int q){
 memset(c,0,sizeof(*c));c->br=br;
 if(br){c->be=BrotliEncoderCreateInstance(NULL,NULL,NULL);c->bd=BrotliDecoderCreateInstance(NULL,NULL,NULL);CHECK(c->be&&c->bd);CHECK(BrotliEncoderSetParameter(c->be,BROTLI_PARAM_QUALITY,q));CHECK(BrotliEncoderSetParameter(c->be,BROTLI_PARAM_LGWIN,22));}
 else{CHECK(deflateInit2(&c->enc,q,Z_DEFLATED,31,8,Z_DEFAULT_STRATEGY)==Z_OK);CHECK(inflateInit2(&c->dec,31)==Z_OK);}
}
static size_t enc(Codec*c,Packet p,uint8_t*out,int finish){
 if(c->br){size_t in=p.n,avail=CAP;const uint8_t*next=p.p;uint8_t*dest=out;CHECK(BrotliEncoderCompressStream(c->be,finish?BROTLI_OPERATION_FINISH:BROTLI_OPERATION_FLUSH,&in,&next,&avail,&dest,NULL));CHECK(in==0&&!BrotliEncoderHasMoreOutput(c->be));return CAP-avail;}
 c->enc.next_in=p.p;c->enc.avail_in=(uInt)p.n;c->enc.next_out=out;c->enc.avail_out=CAP;int r=deflate(&c->enc,finish?Z_FINISH:Z_SYNC_FLUSH);CHECK(r==(finish?Z_STREAM_END:Z_OK));CHECK(c->enc.avail_in==0&&c->enc.avail_out>0);return CAP-c->enc.avail_out;
}
static size_t dec(Codec*c,uint8_t*in,size_t n,uint8_t*out,int finish){
 if(c->br){size_t size=n,avail=CAP;const uint8_t*next=in;uint8_t*dest=out;BrotliDecoderResult r=BrotliDecoderDecompressStream(c->bd,&size,&next,&avail,&dest,NULL);CHECK(r==(finish?BROTLI_DECODER_RESULT_SUCCESS:BROTLI_DECODER_RESULT_NEEDS_MORE_INPUT));CHECK(size==0);return CAP-avail;}
 c->dec.next_in=in;c->dec.avail_in=(uInt)n;c->dec.next_out=out;c->dec.avail_out=CAP;int r=inflate(&c->dec,Z_SYNC_FLUSH);CHECK(r==(finish?Z_STREAM_END:Z_OK));CHECK(c->dec.avail_in==0&&c->dec.avail_out>0);return CAP-c->dec.avail_out;
}
static void destroy(Codec*c){if(c->br){BrotliEncoderDestroyInstance(c->be);BrotliDecoderDestroyInstance(c->bd);}else{CHECK(deflateEnd(&c->enc)==Z_OK);CHECK(inflateEnd(&c->dec)==Z_OK);}}
static void run(Packet*p,int count,int round,int br,int q,int fresh){
 Codec c;uint64_t t=ns();double setup=0; if(!fresh){init(&c,br,q);setup=(ns()-t)/1000.;}
 uint8_t compressed[CAP],decoded[CAP];double *e=calloc(count,sizeof(double)),*d=calloc(count,sizeof(double));CHECK(e&&d);
 size_t bytes=0,raw=0,coldbytes=0;double es=0,ds=0,ce=0,cd=0;int n=0;
 for(int i=0;i<count;i++){
  double construction=0;if(fresh){t=ns();init(&c,br,q);construction=(ns()-t)/1000.;}
  t=ns();size_t size=enc(&c,p[i],compressed,fresh);double et=(ns()-t)/1000.;
  t=ns();size_t len=dec(&c,compressed,size,decoded,fresh);double dt=(ns()-t)/1000.;
  CHECK(len==p[i].n&&!memcmp(decoded,p[i].p,len));
  if(i==0){coldbytes=size;ce=et;cd=dt;}
  if(fresh)destroy(&c);
  if(i>=WARM){if(fresh)setup+=construction;bytes+=size;raw+=len;e[n]=et;d[n]=dt;es+=et;ds+=dt;n++;}
 }
 if(!fresh){size_t tail=enc(&c,(Packet){NULL,0},compressed,1);CHECK(dec(&c,compressed,tail,decoded,1)==0);destroy(&c);}else{setup/=n;}
 printf("{\"round\":%d,\"codec\":\"%s\",\"level\":%d,\"events\":%d,\"raw_bytes\":%.6f,\"compressed_bytes\":%.6f,\"encode_mean_us\":%.6f,\"encode_p50_us\":%.6f,\"encode_p95_us\":%.6f,\"decode_mean_us\":%.6f,\"decode_p50_us\":%.6f,\"decode_p95_us\":%.6f,\"cold_bytes\":%zu,\"cold_encode_us\":%.6f,\"cold_decode_us\":%.6f,\"context_init_us\":%.6f}\n",round,br?"brotli":"gzip",q,n,(double)raw/n,(double)bytes/n,es/n,pct(e,n,50),pct(e,n,95),ds/n,pct(d,n,50),pct(d,n,95),coldbytes,ce,cd,setup);
 free(e);free(d);
}
int main(int argc,char**argv){
 if(argc!=2&&argc!=3){fprintf(stderr,"usage: stream fixture.bin [response]\n");return 2;}
 int fresh=argc==3; if(fresh)CHECK(!strcmp(argv[2],"response"));
 FILE*f=fopen(argv[1],"rb");CHECK(f);CHECK(!fseek(f,0,SEEK_END));long length=ftell(f);CHECK(length>0);rewind(f);uint8_t*data=malloc(length);CHECK(data&&fread(data,1,length,f)==(size_t)length);fclose(f);
 Packet packets[10000];size_t at=0;int count=0;
 while(at<(size_t)length){CHECK(at+4<=(size_t)length&&count<10000);uint32_t n=(uint32_t)data[at]|((uint32_t)data[at+1]<<8)|((uint32_t)data[at+2]<<16)|((uint32_t)data[at+3]<<24);at+=4;CHECK(n>0&&n<CAP/2&&n<=(size_t)length-at);packets[count++]=(Packet){data+at,n};at+=n;}
 CHECK(count>WARM);fprintf(stderr,"zlib=%s brotli_encoder=%u brotli_decoder=%u packets=%d\n",zlibVersion(),BrotliEncoderVersion(),BrotliDecoderVersion(),count);
 int qualities[]={1,6,1,4,6};for(int round=1;round<=5;round++)for(int step=0;step<5;step++){int k=(step+round-1)%5;run(packets,count,round,k>=2,qualities[k],fresh);}
 free(data);return 0;
}
