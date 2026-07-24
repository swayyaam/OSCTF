/*
 * overflow-lite: a deliberately gentle stack overflow.
 *
 * The buffer and the `won` gate live in one struct, laid out in declaration
 * order, so overflowing `buf` writes straight into `won`. Send more than 64
 * bytes to flip it and the program prints $FLAG (injected by the platform).
 *
 * Compiled with -fno-stack-protector -no-pie for approachability.
 */
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

int main(void) {
    setvbuf(stdout, NULL, _IONBF, 0);

    struct {
        char buf[64];
        volatile long won;
    } s;
    s.won = 0;

    puts("Enter the secret password:");
    /* No bounds check: reads up to 256 bytes into a 64-byte buffer. */
    ssize_t n = read(0, s.buf, 256);
    if (n < 0) return 1;

    if (s.won != 0) {
        char *flag = getenv("FLAG");
        printf("Access granted! %s\n", flag ? flag : "OSCTF{no_flag_injected}");
    } else {
        puts("Access denied.");
    }
    return 0;
}
