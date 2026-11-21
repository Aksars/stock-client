// Entry point: flags, the scheduler and the local web server. See store.go for the overview and AUDIT.md.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// inWindow: weekdays 10:05–18:30 Moscow time (the main session of the Moscow Exchange).
func inWindow(t time.Time) bool {
	n := t.In(msk)
	m := n.Hour()*60 + n.Minute()
	return n.Weekday() != time.Saturday && n.Weekday() != time.Sunday && m >= 10*60+5 && m <= 18*60+30
}

// scheduler: in the window, one pass every 5 minutes; outside it, only the decision is refreshed for the interface
// every 30 minutes (one GET to the strategy server, nothing is sent).
func scheduler() {
	last := time.Time{}
	for {
		now := time.Now()
		cfg := loadConfig()
		if inWindow(now) && (cfg.Mode == "sandbox" || cfg.Mode == "live") {
			log.Printf("pass: %s", recordPass())
		} else if now.Sub(last) > 30*time.Minute {
			fetchDecision()
			last = now
		}
		time.Sleep(5 * time.Minute)
	}
}

// openBrowser opens the interface in the default browser (the only local program this client ever starts).
func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		c = exec.Command("open", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	c.Start()
}

func main() {
	exe, _ := os.Executable()
	home := flag.String("home", filepath.Dir(exe), "folder with data/ and secrets/")
	listen := flag.String("listen", "127.0.0.1:8240", "address of the local interface")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser at start")
	noScheduler := flag.Bool("no-scheduler", false, "do not run the scheduler (interface only)")
	flag.Parse()
	homeDir = *home
	os.MkdirAll(filepath.Join(homeDir, "data"), 0o700)
	os.MkdirAll(filepath.Join(homeDir, "secrets"), 0o700)
	if f, err := os.OpenFile(dataPath("app.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(f)
	}
	if h, _, err := net.SplitHostPort(*listen); err == nil {
		ip := net.ParseIP(h)
		allowWideHost = !(h == "localhost" || (ip != nil && ip.IsLoopback()))
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot listen on", *listen, ":", err)
		os.Exit(1)
	}
	url := "http://localhost:" + fmt.Sprint(ln.Addr().(*net.TCPAddr).Port) + "/"
	log.Printf("%s started, home %s, interface %s", Version, homeDir, url)
	fmt.Println("stock-client:", url)
	if !*noScheduler {
		go scheduler()
	}
	if !*noBrowser {
		openBrowser(url)
	}
	srv := &http.Server{Handler: routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}
