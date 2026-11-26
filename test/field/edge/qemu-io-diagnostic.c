#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/uio.h>
#include <time.h>
#include <unistd.h>
/* Pass through unchanged. Log metadata only, never guest bytes or credentials. */
static void report(const char *op,int fd,const struct iovec *v,int n,off64_t off,ssize_t ret,int err) {
 const char *path=getenv("FIELD_IO_DIAGNOSTIC_LOG");if(!path)return;
 size_t bytes=0;int mem512=0,mem4096=0,len4096=0;
 for(int i=0;i<n;i++){bytes+=v[i].iov_len;mem512+=((uintptr_t)v[i].iov_base%512)!=0;mem4096+=((uintptr_t)v[i].iov_base%4096)!=0;len4096+=(v[i].iov_len%4096)!=0;}
 struct timespec t;clock_gettime(CLOCK_REALTIME,&t);char b[1024];
 int len=snprintf(b,sizeof b,"{\"epoch\":%lld.%09ld,\"op\":\"%s\",\"fd\":%d,\"offset\":%lld,\"bytes\":%zu,\"iov_count\":%d,\"memory_unaligned_512\":%d,\"memory_unaligned_4096\":%d,\"iov_length_unaligned_4096\":%d,\"result\":%zd,\"errno\":%d}\n",(long long)t.tv_sec,t.tv_nsec,op,fd,(long long)off,bytes,n,mem512,mem4096,len4096,ret,err);
 int log=open(path,O_WRONLY|O_APPEND|O_CREAT|O_CLOEXEC,0600);if(log>=0){if(len>0&&len<(int)sizeof b)(void)write(log,b,len);close(log);}
}
static pthread_once_t once=PTHREAD_ONCE_INIT;
static ssize_t(*real_pwritev64)(int,const struct iovec*,int,off64_t);
static ssize_t(*real_pwrite64)(int,const void*,size_t,off64_t);
static ssize_t(*real_pread64)(int,void*,size_t,off64_t);
static ssize_t(*real_preadv64)(int,const struct iovec*,int,off64_t);
static int(*real_fdatasync)(int);
static int(*real_fsync)(int);
static void resolve(void){
 real_pwritev64=dlsym(RTLD_NEXT,"pwritev64");real_pwrite64=dlsym(RTLD_NEXT,"pwrite64");real_pread64=dlsym(RTLD_NEXT,"pread64");
 real_preadv64=dlsym(RTLD_NEXT,"preadv64");real_fdatasync=dlsym(RTLD_NEXT,"fdatasync");real_fsync=dlsym(RTLD_NEXT,"fsync");
 if(!real_pwritev64||!real_pwrite64||!real_pread64||!real_preadv64||!real_fdatasync||!real_fsync)_exit(125);
}
ssize_t pwritev64(int fd,const struct iovec *v,int n,off64_t off){
 pthread_once(&once,resolve);ssize_t r=real_pwritev64(fd,v,n,off);int e=errno;
 size_t expected=0;for(int i=0;i<n;i++)expected+=v[i].iov_len;
 if(r<0||(size_t)r!=expected){report("pwritev64",fd,v,n,off,r,r<0?e:0);}
 errno=e;return r;
}
ssize_t pwrite64(int fd,const void *b,size_t n,off64_t off){
 pthread_once(&once,resolve);ssize_t r=real_pwrite64(fd,b,n,off);int e=errno;
 if(r<0||(size_t)r!=n){struct iovec v={(void*)b,n};report("pwrite64",fd,&v,1,off,r,r<0?e:0);}errno=e;return r;
}
ssize_t pread64(int fd,void *b,size_t n,off64_t off){
 static unsigned calls;pthread_once(&once,resolve);ssize_t r=real_pread64(fd,b,n,off);int e=errno;
 if(off==0&&n<=4096&&__atomic_fetch_add(&calls,1,__ATOMIC_RELAXED)<48){struct iovec v={b,n};report("pread64-probe",fd,&v,1,off,r,r<0?e:0);}
 else if(r<0){struct iovec v={b,n};report("pread64",fd,&v,1,off,r,e);}
 errno=e;return r;
}

ssize_t preadv64(int fd,const struct iovec *v,int n,off64_t off){
 pthread_once(&once,resolve);ssize_t r=real_preadv64(fd,v,n,off);int e=errno;
 if(r<0){report("preadv64",fd,v,n,off,r,e);}
 errno=e;return r;
}
int fdatasync(int fd){
 pthread_once(&once,resolve);int r=real_fdatasync(fd);int e=errno;
 if(r<0){report("fdatasync",fd,NULL,0,-1,r,e);}errno=e;return r;
}
int fsync(int fd){
 pthread_once(&once,resolve);int r=real_fsync(fd);int e=errno;
 if(r<0){report("fsync",fd,NULL,0,-1,r,e);}errno=e;return r;
}
