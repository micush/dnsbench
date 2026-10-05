// Minimal DNS reflector for benchmarking dnsbench itself.
//
// Echoes every UDP datagram back with the QR bit set, using recvmmsg/sendmmsg,
// so the responder is not the bottleneck. NOT a DNS server.
//
//   gcc -O2 -o reflect reflect.c
//   ./reflect 5399 &            # listens on 127.0.0.1:5399
//   python3 drive.py http://127.0.0.1:8453 benchuser 'password' 127.0.0.1:5399 2 64 10s
//
// Start it with setsid/nohup and a pid file, and check it is still alive
// before every run: a dead reflector looks exactly like a slow engine.
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>
#define B 64
int main(int argc, char **argv) {
    int port = argc > 1 ? atoi(argv[1]) : 5399;
    int fd = socket(AF_INET, SOCK_DGRAM, 0);
    int sz = 8 << 20;
    setsockopt(fd, SOL_SOCKET, SO_RCVBUFFORCE, &sz, sizeof sz);
    setsockopt(fd, SOL_SOCKET, SO_SNDBUFFORCE, &sz, sizeof sz);
    struct sockaddr_in a = {.sin_family = AF_INET, .sin_port = htons(port)};
    a.sin_addr.s_addr = inet_addr("127.0.0.1");
    if (bind(fd, (void *)&a, sizeof a)) { perror("bind"); return 1; }
    static char buf[B][512];
    static struct sockaddr_in from[B];
    struct iovec iov[B];
    struct mmsghdr m[B];
    for (;;) {
        for (int i = 0; i < B; i++) {
            iov[i].iov_base = buf[i]; iov[i].iov_len = 512;
            memset(&m[i], 0, sizeof m[i]);
            m[i].msg_hdr.msg_name = &from[i];
            m[i].msg_hdr.msg_namelen = sizeof from[i];
            m[i].msg_hdr.msg_iov = &iov[i];
            m[i].msg_hdr.msg_iovlen = 1;
        }
        int n = recvmmsg(fd, m, B, MSG_WAITFORONE, NULL);
        if (n <= 0) continue;
        for (int i = 0; i < n; i++) {
            buf[i][2] |= 0x80;
            iov[i].iov_len = m[i].msg_len;
            m[i].msg_hdr.msg_namelen = sizeof from[i];
        }
        int sent = 0;
        while (sent < n) {
            int r = sendmmsg(fd, m + sent, n - sent, 0);
            if (r <= 0) break;
            sent += r;
        }
    }
}
