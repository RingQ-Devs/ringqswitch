/*
 * faxpeer: a spandsp fax terminal for RingQswitch's fax tests (not part
 * of RingQswitch itself). It reads 8 kHz 16-bit audio from stdin and
 * writes as much to stdout, 160 samples at a time, so a test drives it
 * sample-exactly, as if it were the far end of a G.711 call.
 *
 *   faxpeer send <file.tif> | faxpeer receive <out.tif>
 *
 * Environment: FAXPEER_MODEMS (bit mask: 1 V.27ter, 2 V.29, 4 V.17;
 * default 3), FAXPEER_ECM=1, FAXPEER_LOG=1, FAXPEER_IDENT, FAXPEER_ANSWER=1
 * (a sender that answers instead of calling).
 * stderr: "RESULT <code> <text> pages=<n> rate=<bps> bad_rows=<n>".
 *
 * T.38: "faxpeer t38send <file.tif>" / "faxpeer t38receive <out.tif>" run
 * spandsp's T.38 terminal instead. Each 20 ms the test writes the UDPTL
 * datagrams of that time, each as a 2-byte little-endian length and the
 * bytes, then a zero length; faxpeer answers the same way. The UDPTL
 * framing (with redundancy) is written here, independently of
 * RingQswitch's. FAXPEER_T38_VERSION (default 0), FAXPEER_REDUNDANCY
 * (previous packets repeated in each datagram, default 3), FAXPEER_DROP=n
 * (drop every n-th datagram both ways, to exercise the recovery).
 *
 * Build: cc -O2 -o faxpeer faxpeer.c -lspandsp
 */
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <spandsp.h>

static int done;

static void log_to_stderr(int level, const char *text)
{
    (void)level;
    fputs(text, stderr);
}

static void phase_e(t30_state_t *s, void *user, int completion)
{
    t30_stats_t st;
    (void)user;
    t30_get_transfer_statistics(s, &st);
    fprintf(stderr, "RESULT %d %s pages=%d rate=%d bad_rows=%d\n", completion, t30_completion_code_to_str(completion),
            st.pages_tx > st.pages_rx ? st.pages_tx : st.pages_rx, st.bit_rate, st.bad_rows);
    fflush(stderr);
    done = 1;
}

/* ---- UDPTL (T.38 clause 9.1), redundancy only */

#define HIST 16
static uint8_t tx_hist[HIST][512];
static int tx_hist_len[HIST];
static int tx_entries, tx_seq, redundancy = 3, drop_every, dgram_no;
static uint8_t outbuf[65536];
static int outlen;
static int rx_next = -1;

static int put_len(uint8_t *b, int n)
{
    if (n < 0x80) {
        b[0] = (uint8_t)n;
        return 1;
    }
    b[0] = (uint8_t)(0x80 | (n >> 8));
    b[1] = (uint8_t)n;
    return 2;
}

static int get_len(const uint8_t *b, int len, int *i, int *n)
{
    if (*i >= len)
        return -1;
    if ((b[*i] & 0x80) == 0) {
        *n = b[(*i)++];
        return 0;
    }
    if ((b[*i] & 0x40) || *i + 1 >= len)
        return -1;
    *n = ((b[*i] & 0x3F) << 8) | b[*i + 1];
    *i += 2;
    return 0;
}

static void udptl_send(const uint8_t *ifp, int len)
{
    uint8_t d[2048];
    int n = 0, i, k, entries;

    d[n++] = (uint8_t)(tx_seq >> 8);
    d[n++] = (uint8_t)tx_seq;
    n += put_len(d + n, len);
    memcpy(d + n, ifp, len);
    n += len;
    d[n++] = 0x00; /* secondary-ifp-packets */
    entries = tx_entries < redundancy ? tx_entries : redundancy;
    n += put_len(d + n, entries);
    for (i = 0; i < entries; i++) {
        k = (tx_seq - 1 - i) & (HIST - 1);
        n += put_len(d + n, tx_hist_len[k]);
        memcpy(d + n, tx_hist[k], tx_hist_len[k]);
        n += tx_hist_len[k];
    }
    k = tx_seq & (HIST - 1);
    memcpy(tx_hist[k], ifp, len);
    tx_hist_len[k] = len;
    tx_entries++;
    tx_seq = (tx_seq + 1) & 0xFFFF;
    if (drop_every && ++dgram_no % drop_every == 0)
        return;
    outbuf[outlen++] = (uint8_t)n;
    outbuf[outlen++] = (uint8_t)(n >> 8);
    memcpy(outbuf + outlen, d, n);
    outlen += n;
}

static int tx_packet(t38_core_state_t *s, void *user, const uint8_t *buf, int len, int count)
{
    (void)s;
    (void)user;
    while (count-- > 0)
        udptl_send(buf, len);
    return 0;
}

static void udptl_receive(t38_core_state_t *core, const uint8_t *d, int len)
{
    int i = 2, n, seq, count, k, gap;
    const uint8_t *sec[HIST];
    int sec_len[HIST], nsec = 0;
    const uint8_t *primary;
    int primary_len;

    if (len < 3)
        return;
    seq = (d[0] << 8) | d[1];
    if (get_len(d, len, &i, &n) || i + n > len)
        return;
    primary = d + i;
    primary_len = n;
    i += n;
    if (i < len && (d[i] & 0x80) == 0) {
        i++;
        if (get_len(d, len, &i, &count) == 0) {
            for (k = 0; k < count && nsec < HIST; k++) {
                if (get_len(d, len, &i, &n) || i + n > len)
                    break;
                sec[nsec] = d + i;
                sec_len[nsec++] = n;
                i += n;
            }
        }
    }
    if (rx_next < 0)
        rx_next = seq;
    gap = (seq - rx_next) & 0xFFFF;
    if (gap >= 0x8000)
        return;
    for (k = gap - 1; k >= 0; k--)
        if (k < nsec)
            t38_core_rx_ifp_packet(core, sec[k], sec_len[k], (uint16_t)(seq - 1 - k));
    t38_core_rx_ifp_packet(core, primary, primary_len, (uint16_t)seq);
    rx_next = (seq + 1) & 0xFFFF;
}

static int read_full(uint8_t *b, int n)
{
    return fread(b, 1, n, stdin) == (size_t)n;
}

static int t38_main(int sending, const char *file)
{
    const char *env;
    t38_terminal_state_t *t38;
    t38_core_state_t *core;
    t30_state_t *t30;
    int after = 0;
    uint8_t lb[2], d[4096];

    redundancy = (env = getenv("FAXPEER_REDUNDANCY")) ? atoi(env) : 3;
    drop_every = (env = getenv("FAXPEER_DROP")) ? atoi(env) : 0;
    t38 = t38_terminal_init(NULL, sending && !getenv("FAXPEER_ANSWER"), tx_packet, NULL);
    t30 = t38_terminal_get_t30_state(t38);
    core = t38_terminal_get_t38_core_state(t38);
    t38_set_t38_version(core, (env = getenv("FAXPEER_T38_VERSION")) ? atoi(env) : 0);
    span_set_message_handler(log_to_stderr);
    if (getenv("FAXPEER_LOG")) {
        span_log_set_message_handler(t30_get_logging_state(t30), log_to_stderr);
        span_log_set_level(t30_get_logging_state(t30), SPAN_LOG_SHOW_SEVERITY | SPAN_LOG_SHOW_PROTOCOL | SPAN_LOG_FLOW);
        span_log_set_message_handler(t38_core_get_logging_state(core), log_to_stderr);
        span_log_set_level(t38_core_get_logging_state(core), SPAN_LOG_SHOW_SEVERITY | SPAN_LOG_SHOW_PROTOCOL | SPAN_LOG_FLOW);
    }
    if (sending)
        t30_set_tx_file(t30, file, -1, -1);
    else
        t30_set_rx_file(t30, file, -1);
    t30_set_tx_ident(t30, (env = getenv("FAXPEER_IDENT")) ? env : "SPANDSP PEER");
    t30_set_ecm_capability(t30, getenv("FAXPEER_ECM") != NULL);
    t30_set_supported_modems(t30, (env = getenv("FAXPEER_MODEMS")) ? atoi(env) : (T30_SUPPORT_V27TER | T30_SUPPORT_V29));
    t30_set_phase_e_handler(t30, phase_e, NULL);

    for (;;) {
        int n;
        for (;;) {
            if (!read_full(lb, 2))
                goto out;
            n = lb[0] | (lb[1] << 8);
            if (n == 0)
                break;
            if (n > (int)sizeof(d) || !read_full(d, n))
                goto out;
            if (drop_every && ++dgram_no % drop_every == 0)
                continue;
            udptl_receive(core, d, n);
        }
        t38_terminal_send_timeout(t38, 160);
        outbuf[outlen++] = 0;
        outbuf[outlen++] = 0;
        if (fwrite(outbuf, 1, outlen, stdout) != (size_t)outlen)
            break;
        outlen = 0;
        fflush(stdout);
        if (done && ++after > 25)
            break;
    }
out:
    fprintf(stderr, "END\n");
    return 0;
}

int main(int argc, char **argv)
{
    int sending, calling, after = 0;
    const char *env;
    fax_state_t *fax;
    t30_state_t *t30;
    int16_t in[160], out[160];

    if (argc == 3 && (!strcmp(argv[1], "t38send") || !strcmp(argv[1], "t38receive")))
        return t38_main(!strcmp(argv[1], "t38send"), argv[2]);
    if (argc < 3 || (strcmp(argv[1], "send") && strcmp(argv[1], "receive"))) {
        fprintf(stderr, "usage: faxpeer send|receive|t38send|t38receive <file.tif>\n");
        return 2;
    }
    sending = strcmp(argv[1], "send") == 0;
    calling = sending && !getenv("FAXPEER_ANSWER");
    fax = fax_init(NULL, calling);
    t30 = fax_get_t30_state(fax);
    fax_set_transmit_on_idle(fax, 1);
    span_set_message_handler(log_to_stderr);
    if (getenv("FAXPEER_LOG")) {
        span_log_set_message_handler(t30_get_logging_state(t30), log_to_stderr);
        span_log_set_level(t30_get_logging_state(t30), SPAN_LOG_SHOW_SEVERITY | SPAN_LOG_SHOW_PROTOCOL | SPAN_LOG_FLOW);
        span_log_set_message_handler(fax_get_logging_state(fax), log_to_stderr);
        span_log_set_level(fax_get_logging_state(fax), SPAN_LOG_SHOW_SEVERITY | SPAN_LOG_SHOW_PROTOCOL | SPAN_LOG_FLOW);
    }
    if (sending)
        t30_set_tx_file(t30, argv[2], -1, -1);
    else
        t30_set_rx_file(t30, argv[2], -1);
    t30_set_tx_ident(t30, (env = getenv("FAXPEER_IDENT")) ? env : "SPANDSP PEER");
    t30_set_ecm_capability(t30, getenv("FAXPEER_ECM") != NULL);
    t30_set_supported_modems(t30, (env = getenv("FAXPEER_MODEMS")) ? atoi(env) : (T30_SUPPORT_V27TER | T30_SUPPORT_V29));
    t30_set_phase_e_handler(t30, phase_e, NULL);

    while (fread(in, sizeof(int16_t), 160, stdin) == 160) {
        int n;
        fax_rx(fax, in, 160);
        n = fax_tx(fax, out, 160);
        if (n < 160)
            memset(out + n, 0, (160 - n) * sizeof(int16_t));
        if (fwrite(out, sizeof(int16_t), 160, stdout) != 160)
            break;
        fflush(stdout);
        if (done && ++after > 25)
            break;
    }
    fprintf(stderr, "END\n");
    return 0;
}
