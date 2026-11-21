/* SPDX-License-Identifier: Apache-2.0 */
/* Keep envd's command policy without three extra executable loads. */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <unistd.h>

static int failure(const char *operation) {
    perror(operation);
    return 125;
}
int main(int argc, char **argv) {
    if (argc < 2) { fputs("exec-wrapper: command required\n", stderr); return 125; }
    int fd = open("/proc/self/oom_score_adj", O_WRONLY | O_CLOEXEC);
    if (fd < 0) return failure("exec-wrapper: oom_score_adj");
    if (write(fd, "100\n", 4) != 4) { close(fd); return failure("exec-wrapper: write oom_score_adj"); }
    if (close(fd)) return failure("exec-wrapper: close oom_score_adj");
    /* Linux IOPRIO_WHO_PROCESS=1, IOPRIO_CLASS_BE=2, priority=4. */
    if (syscall(SYS_ioprio_set, 1, 0, (2 << 13) | 4)) return failure("exec-wrapper: ioprio_set");
    if (setpriority(PRIO_PROCESS, 0, 0)) perror("exec-wrapper: cannot set niceness");
    execvp(argv[1], argv + 1);
    int code = errno == ENOENT ? 127 : 126;
    perror("exec-wrapper: exec");
    return code;
}
