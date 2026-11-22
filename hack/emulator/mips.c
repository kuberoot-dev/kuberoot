// A hot two-instruction RV64 loop, for a rough upper-bound calibration only.
// It excludes memory, TLS, Go allocation and kernel work; not a CPU benchmark.
#include <stdio.h>
#include <time.h>
int main(void) {
    struct timespec a, b;
    unsigned long iterations = 100000000;
    clock_gettime(CLOCK_MONOTONIC, &a);
    __asm__ volatile ("1: addi %0, %0, -1\n\tbnez %0, 1b" : "+r" (iterations));
    clock_gettime(CLOCK_MONOTONIC, &b);
    double elapsed = b.tv_sec-a.tv_sec + (b.tv_nsec-a.tv_nsec)/1e9;
    printf("{\"instructions\":200000000,\"elapsed_s\":%.6f,\"hot_loop_mips\":%.3f}\n", elapsed, 200/elapsed);
    return 0;
}
