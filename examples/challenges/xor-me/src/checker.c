/*
 * xor-me: a small reversing challenge. The flag is stored XORed with a fixed
 * key. At runtime the checker XORs the stored bytes back and compares them to
 * your input. Reversing = find `stored[]` and `key[]` in the binary and XOR.
 *
 * Built reproducibly by `make examples` (see scripts/build-examples.sh).
 */
#include <stdio.h>
#include <string.h>

static const unsigned char stored[] = {
    0x20, 0x20, 0x20, 0x20, 0x20, 0x56, 0x13, 0x55, 0x0b, 0x30, 0x1a,
    0x10, 0x2b, 0x14, 0x48, 0x1d, 0x00, 0x0b, 0x1c, 0x1a, 0x01, 0x18,
    0x03, 0x72, 0x04, 0x07, 0x0f, 0x06, 0x1c, 0x16, 0x07, 0x0a, 0x54,
    0x16,
};
static const char key[] = "osctf-key";

int main(void) {
    char input[128];
    printf("Enter the flag: ");
    if (!fgets(input, sizeof(input), stdin)) return 1;
    input[strcspn(input, "\r\n")] = '\0';

    size_t n = sizeof(stored);
    if (strlen(input) != n) {
        puts("Nope.");
        return 1;
    }
    for (size_t i = 0; i < n; i++) {
        unsigned char expected = stored[i] ^ (unsigned char)key[i % (sizeof(key) - 1)];
        if ((unsigned char)input[i] != expected) {
            puts("Nope.");
            return 1;
        }
    }
    puts("Correct! That's the flag.");
    return 0;
}
