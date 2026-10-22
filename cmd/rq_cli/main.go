// Command rq_cli is the RingQswitch console: it
// connects to the event socket (the password is read from ringqswitch.xml,
// so none needs typing), shows the live log in colour and runs commands.
//
//	rq_cli                       console with the live log (debug)
//	rq_cli -x "show channels"    run one command and exit
//	rq_cli -l info               console with the log from info up
//	rq_cli -q                    console without the log
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/esl"
	"github.com/Cal4Care-Developers/RingQswitch/internal/logbus"
)

var logo = []string{
	" ____   _                 ___                  _  _          _     ",
	"|  _ \\ (_) _ __    __ _  / _ \\  ___ __      __(_)| |_   ___ | |__  ",
	"| |_) || || '_ \\  / _` || | | |/ __|\\ \\ /\\ / /| || __| / __|| '_ \\ ",
	"|  _ < | || | | || (_| || |_| |\\__ \\ \\ V  V / | || |_ | (__ | | | |",
	"|_| \\_\\|_||_| |_| \\__, | \\__\\_\\|___/  \\_/\\_/  |_| \\__| \\___||_| |_|",
	"                  |___/                                            ",
}

// the console's colours: DEBUG yellow, INFO green, NOTICE cyan, WARNING
// magenta, ERR and worse red.
var colours = map[int]string{0: "\x1b[0m", 1: "\x1b[31m", 2: "\x1b[31m", 3: "\x1b[31m", 4: "\x1b[35m", 5: "\x1b[36m", 6: "\x1b[32m", 7: "\x1b[33m"}

const reset = "\x1b[0m"

func main() {
	host := flag.String("H", "", "event socket host (default: from ringqswitch.xml, else 127.0.0.1)")
	port := flag.Int("P", 0, "event socket port (default: from ringqswitch.xml, else 8021)")
	password := flag.String("p", os.Getenv("RQ_ESL_PASSWORD"), "event socket password (default: $RQ_ESL_PASSWORD, else read from ringqswitch.xml)")
	confDir := flag.String("conf", "", "configuration directory to read the event socket address and password from (default /etc/ringqswitch)")
	execute := flag.String("x", "", "execute one command and exit")
	level := flag.String("l", "debug", "log level to show: debug, info, notice, warning, err (or 7..0)")
	quiet := flag.Bool("q", false, "do not show the log")
	noColour := flag.Bool("n", false, "no colours")
	flag.Parse()

	addr, pw := fromConfig(*confDir)
	if *password != "" {
		pw = *password
	}
	h, p := "127.0.0.1", 8021
	if a, b, err := net.SplitHostPort(addr); err == nil {
		if a != "" && a != "0.0.0.0" && a != "::" {
			h = a
		}
		if n, err := strconv.Atoi(b); err == nil {
			p = n
		}
	}
	if *host != "" {
		h = *host
	}
	if *port != 0 {
		p = *port
	}
	if pw == "" {
		fmt.Fprintln(os.Stderr, "rq_cli: no event socket password: run as root (it is read from /etc/ringqswitch/ringqswitch.xml) or give -p")
		os.Exit(1)
	}
	c, err := esl.Dial(net.JoinHostPort(h, strconv.Itoa(p)), pw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rq_cli: cannot connect to RingQswitch at %s:%d: %v\n", h, p, err)
		os.Exit(1)
	}
	defer c.Close()

	if *execute != "" {
		out, err := c.API(*execute)
		if err != nil {
			fmt.Fprintln(os.Stderr, "rq_cli:", err)
			os.Exit(1)
		}
		fmt.Print(out)
		if out != "" && !strings.HasSuffix(out, "\n") {
			fmt.Println()
		}
		if strings.HasPrefix(out, "-ERR") {
			os.Exit(1)
		}
		return
	}

	colour := !*noColour && isTerminal()
	version, _ := c.API("version")
	hostname := strings.TrimSpace(apiOr(c, "hostname", h))
	banner(strings.TrimSpace(version), hostname, h, p)
	prompt := "ringqswitch@" + hostname + "> "

	con := &console{prompt: prompt, colour: colour, tty: isTerminal()}
	if !*quiet {
		n, ok := logbus.ParseLevel(*level)
		if !ok {
			n = 7
		}
		m, err := c.Send(fmt.Sprintf("log %d", n))
		con.reply(m, err)
	}
	go con.printLogs(c)
	go con.printEvents(c)

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64*1024), 1024*1024)
	for {
		con.showPrompt()
		if !in.Scan() {
			fmt.Println()
			_, _ = c.Send("exit")
			return
		}
		con.typed()
		line := strings.TrimSpace(in.Text())
		f := strings.Fields(line)
		switch {
		case line == "":
			continue
		case line == "/exit" || line == "/quit" || line == "/bye" || line == "exit" || line == "quit" || line == "...":
			_, _ = c.Send("exit")
			return
		case line == "/help":
			help()
		case f[0] == "/log":
			arg := "debug"
			if len(f) > 1 {
				arg = f[1]
			}
			n, ok := logbus.ParseLevel(arg)
			if !ok {
				fmt.Println("-ERR log level: debug, info, notice, warning, err, crit (or 7..0)")
				continue
			}
			m, err := c.Send(fmt.Sprintf("log %d", n))
			con.reply(m, err)
		case line == "/nolog":
			m, err := c.Send("nolog")
			con.reply(m, err)
		case f[0] == "/event" || f[0] == "/events":
			args := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "/events"), "/event"))
			if args == "" {
				args = "plain ALL"
			}
			m, err := c.Send("event " + args)
			con.reply(m, err)
		case line == "/noevents":
			m, err := c.Send("noevents")
			con.reply(m, err)
		case strings.HasPrefix(line, "/"):
			fmt.Println("-ERR unknown command; /help lists them")
		default:
			out, err := c.API(line)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return
			}
			con.print(out)
		}
		select {
		case <-c.Closed():
			fmt.Println("Disconnected from RingQswitch.")
			return
		default:
		}
	}
}

func apiOr(c *esl.Client, cmd, def string) string {
	out, err := c.API(cmd)
	if err != nil || out == "" || strings.HasPrefix(out, "-ERR") {
		return def
	}
	return out
}

// fromConfig reads event-socket-listen and event-socket-password from
// ringqswitch.xml (the service's own configuration).
func fromConfig(dir string) (addr, password string) {
	dirs := []string{dir}
	if dir == "" {
		dirs = []string{"/etc/ringqswitch", "./conf"}
	}
	for _, d := range dirs {
		if _, err := os.Stat(d + "/ringqswitch.xml"); err != nil {
			continue
		}
		if cfg, err := config.Load(d); err == nil {
			return cfg.Settings.ESLListen, cfg.Settings.ESLPassword
		}
		// The full configuration did not load: read the two settings.
		data, err := os.ReadFile(d + "/ringqswitch.xml")
		if err != nil {
			continue
		}
		get := func(name string) string {
			m := regexp.MustCompile(`name="` + name + `"\s+value="([^"]*)"`).FindSubmatch(data)
			if m == nil {
				return ""
			}
			return string(m[1])
		}
		return get("event-socket-listen"), get("event-socket-password")
	}
	return "", ""
}

func banner(version, hostname, h string, p int) {
	width := len(logo[0]) + 6
	line := "." + strings.Repeat("=", width) + "."
	row := func(s string) { fmt.Printf("|%s|\n", pad("   "+s, width)) }
	fmt.Println(line)
	for _, l := range logo {
		row(l)
	}
	row("")
	fmt.Println(line)
	row("RingQswitch console")
	row(fmt.Sprintf("%s on %s, event socket %s:%d", version, hostname, h, p))
	fmt.Println(line)
	fmt.Println()
	fmt.Println("Type /help <enter> to see a list of commands")
	fmt.Println()
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

func help() {
	fmt.Print(`Console commands:
  /help             this list
  /log [level]      show the live log from this level up: debug (7), info (6),
                    notice (5), warning (4), err (3), crit (2)
  /nolog            stop showing the log
  /events [names]   show events too (default: plain ALL), /noevents to stop
  /exit, /quit, /bye, Ctrl+D   leave rq_cli (RingQswitch keeps running)

Everything else is sent to RingQswitch, for example:
  status            show channels     show calls        show registrations
  sip status        sip status profile internal reg     sip status gateway NAME
  reloadxml         uuid_kill UUID    originate user/1000 &echo()
  callcenter_config agent list        callcenter_config queue list
  conference list   version           help  (all the server's commands)
`)
}

// console prints log lines and command output without tearing the prompt.
type console struct {
	mu      sync.Mutex
	prompt  string
	colour  bool
	tty     bool
	waiting bool // the prompt is shown and nothing typed yet
	timer   *time.Timer
}

func (o *console) showPrompt() {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Print(o.prompt)
	o.waiting = true
}

func (o *console) typed() {
	o.mu.Lock()
	o.waiting = false
	o.mu.Unlock()
}

func (o *console) print(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Print(s)
	if s != "" && !strings.HasSuffix(s, "\n") {
		fmt.Println()
	}
}

func (o *console) reply(m *esl.Message, err error) {
	if err != nil {
		o.print("error: " + err.Error() + "\n")
		return
	}
	o.print(m.Headers["Reply-Text"] + "\n")
}

// out writes lines that arrive while the prompt waits: they go above it,
// and the prompt comes back once the burst is over.
func (o *console) out(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.waiting && o.tty {
		fmt.Print("\r\x1b[K") // the log line goes where the prompt was
	} else if o.waiting {
		fmt.Println()
	}
	fmt.Print(text)
	if o.waiting {
		if o.timer != nil {
			o.timer.Stop()
		}
		o.timer = time.AfterFunc(150*time.Millisecond, func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			if o.waiting {
				fmt.Print(o.prompt)
			}
		})
	}
}

func (o *console) printLogs(c *esl.Client) {
	for m := range c.Logs() {
		text := m.Body
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if o.colour {
			n, _ := strconv.Atoi(m.Headers["Log-Level"])
			text = colours[n] + strings.TrimSuffix(text, "\n") + reset + "\n"
		}
		o.out(text)
	}
}

func (o *console) printEvents(c *esl.Client) {
	for m := range c.Events() {
		if m.Type() == "text/event-json" {
			o.out("[event] " + m.Body + "\n")
			continue
		}
		h := m.EventHeaders()
		keys := make([]string, 0, len(h))
		for k := range h {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		name := h["Event-Name"]
		if sub := h["Event-Subclass"]; sub != "" {
			name += " " + sub
		}
		var b strings.Builder
		fmt.Fprintf(&b, "[EVENT] %s", name)
		for _, k := range []string{"Unique-ID", "Channel-Name", "Caller-Caller-ID-Number", "Caller-Destination-Number", "Answer-State", "Hangup-Cause", "User", "Contact"} {
			if v := h[k]; v != "" {
				fmt.Fprintf(&b, " %s=%s", k, v)
			}
		}
		o.out(b.String() + "\n")
	}
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
