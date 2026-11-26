#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/uio.h>
#include <unistd.h>
int main(int argc,char **argv) {
 if(argc!=2)return 2;
 int fd=open(argv[1],O_CREAT|O_EXCL|O_RDWR,0600);if(fd<0){perror("create scratch");return 2;}
 void *base=NULL;if(posix_memalign(&base,4096,65536))return 2;memset(base,0x5a,65536);
 if(write(fd,base,65536)!=65536||fsync(fd)){perror("initialize");return 2;}close(fd);
 struct statx s={0};int rc=statx(AT_FDCWD,argv[1],0,STATX_DIOALIGN,&s);
 printf("statx rc=%d mask=%x mem_align=%u offset_align=%u\n",rc,s.stx_mask,s.stx_dio_mem_align,s.stx_dio_offset_align);
 fd=open(argv[1],O_RDWR|O_DIRECT);if(fd<0){perror("direct open");return 2;}
 const size_t sizes[]={512,1024,2048,4096};const size_t shifts[]={0,512,1024,2048};
 for(int op=0;op<2;op++)for(int b=0;b<4;b++)for(int n=0;n<4;n++)for(int o=0;o<4;o++){
  errno=0;ssize_t r=op?pwrite(fd,(char*)base+shifts[b],sizes[n],shifts[o]):pread(fd,(char*)base+shifts[b],sizes[n],shifts[o]);int e=errno;
  printf("%s buffer_mod4096=%zu offset=%zu length=%zu result=%zd errno=%d %s\n",op?"pwrite":"pread",shifts[b],shifts[o],sizes[n],r,e,e?strerror(e):"OK");
 }
 for(int split=512;split<=4096;split+=512){
  struct iovec v[2]={{base,split},{(char*)base+split,8192-split}};
  errno=0;ssize_t r=pwritev(fd,v,2,8192);int e=errno;
  printf("pwritev offset=8192 lengths=%d,%d result=%zd errno=%d %s\n",split,8192-split,r,e,e?strerror(e):"OK");
 }
 fsync(fd);close(fd);free(base);return 0;
}
