package main

import (
	"flag"
	"net"
	"os"
	"net/http"
	"strings"
	"regexp"
	"github.com/bogdanovich/dns_resolver"
	"fmt"
	"github.com/robfig/cron"
	"io"
	"github.com/ximply/ping_exporter/ping"
	"sync"
)

var (
	Name           = "ping_exporter"
	listenAddress  = flag.String("unix-sock", "/dev/shm/ping_exporter.sock", "Address to listen on for unix sock access and telemetry.")
	metricsPath    = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	dest           = flag.String("dest", "", "Destination list to ping, multi split with ,.")
	count          = flag.Int("count", 5, "How many packages to ping.")
)

type DestAddr struct {
	Ip bool
	Addr string
	Domain string
}

var lock sync.RWMutex
var g_keyList string
var g_statMap map[string]ping.PingSt
var destList []DestAddr
var doing bool

func isIP4(ip string) bool {
	b, _ := regexp.MatchString(`((25[0-5]|2[0-4]\d|((1\d{2})|([1-9]?\d)))\.){3}(25[0-5]|2[0-4]\d|((1\d{2})|([1-9]?\d)))`, ip)
	return b
}

func isDomain(domain string) bool {
	b, _ := regexp.MatchString(`[a-zA-Z0-9][-a-zA-Z0-9]{0,62}(.[a-zA-Z0-9][-a-zA-Z0-9]{0,62})+.?`, domain)
	return b
}

func doWork() {
	if doing {
		return
	}
	doing = true

	resolver, _ := dns_resolver.NewFromResolvConf("/etc/resolv.conf")
	resolver.RetryTimes = 1
	var dl []string
	var keyList string
	var ipMap map[string]string
	ipMap = make(map[string]string)
	for _, target := range destList {
		if target.Ip {
			dl = append(dl, fmt.Sprintf("%s|%s", target.Domain, target.Addr))
			keyList = keyList + fmt.Sprintf(",%s|%s", target.Domain, target.Addr)
			ipMap[target.Addr] = target.Addr
		} else {
			ipList, err := resolver.LookupHost(target.Domain)
			if err == nil {
				for _, ip := range ipList {
					var t = DestAddr{
						Ip: true,
						Addr: ip.String(),
						Domain: target.Domain,
					}
					dl = append(dl, fmt.Sprintf("%s|%s", target.Domain, t.Addr))
					keyList = keyList + fmt.Sprintf(",%s|%s", target.Domain, t.Addr)
					ipMap[t.Addr] = t.Addr
				}
			}
		}
	}

	lock.Lock()
	g_keyList = keyList
	lock.Unlock()

	var pingStatMap map[string]ping.PingSt
	pingStatMap = make(map[string]ping.PingSt)
	for k, _ := range ipMap {
		stat := &ping.PingSt{}
		//ping.StartPing(k, *count, stat)
		ping.SystemCmdPing(k, *count, stat)
		pingStatMap[k] = *stat
	}

	for _, i := range dl {
		l := strings.Split(i, "|")
		if v, ok := pingStatMap[l[1]]; ok {
			lock.Lock()
			g_statMap[i] = v
			lock.Unlock()
		}
	}

	doing = false
}

func metrics(w http.ResponseWriter, r *http.Request) {
	lock.RLock()
	keyList := strings.Split(g_keyList, ",")
	m := g_statMap
	lock.RUnlock()

	ret := ""
	namespace := "ping"
	for _, k := range keyList {
		if v, ok := m[k]; ok {
			l := strings.Split(k, "|")
			ret += fmt.Sprintf("%s_max_delay{domain=\"%s\",addr=\"%s\"} %g\n",
				namespace, l[0], l[1], v.MaxDelay)
			ret += fmt.Sprintf("%s_min_delay{domain=\"%s\",addr=\"%s\"} %g\n",
				namespace, l[0], l[1], v.MinDelay)
			ret += fmt.Sprintf("%s_avg_delay{domain=\"%s\",addr=\"%s\"} %g\n",
				namespace, l[0], l[1], v.AvgDelay)
			ret += fmt.Sprintf("%s_send{domain=\"%s\",addr=\"%s\"} %g\n",
				namespace, l[0], l[1], float64(v.SendPk))
			ret += fmt.Sprintf("%s_lost{domain=\"%s\",addr=\"%s\"} %g\n",
				namespace, l[0], l[1], float64(v.LossPk))
		}
	}

	io.WriteString(w, ret)
}

func main() {
	flag.Parse()

	addr := "/dev/shm/ping_exporter.sock"
	if listenAddress != nil {
		addr = *listenAddress
	}

	if dest == nil || len(*dest) == 0 {
		panic("error dest")
	}
	l := strings.Split(*dest, ",")
	for _, i := range l {
		var d DestAddr
		d.Addr = i
		if isIP4(i) {
			d.Ip = true
			d.Domain = i
			destList = append(destList, d)
		} else if isDomain(i) {
			d.Ip = false
			d.Domain = i
			destList = append(destList, d)
		}
	}

	if len(destList) == 0 {
		panic("no one to ping")
	}

	doing = false
	g_statMap = make(map[string]ping.PingSt)
	doWork()
	c := cron.New()
	c.AddFunc("0 */1 * * * ?", doWork)
	c.Start()

	mux := http.NewServeMux()
	mux.HandleFunc(*metricsPath, metrics)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Ping Exporter</title></head>
             <body>
             <h1>Ping Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})
	server := http.Server{
		Handler: mux, // http.DefaultServeMux,
	}
	os.Remove(addr)

	listener, err := net.Listen("unix", addr)
	if err != nil {
		panic(err)
	}
	server.Serve(listener)
}