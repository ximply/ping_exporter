package ping

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"math/rand"
	"encoding/binary"
	"github.com/ximply/ping_exporter/cache"
	"fmt"
)

type PingSt struct {
	SendPk   int
	RevcPk   int
	LossPk   int
	MinDelay float64
	AvgDelay float64
	MaxDelay float64
}

type pkg struct {
	conn     net.PacketConn
	ipv4conn *ipv4.PacketConn
	msg      icmp.Message
	netmsg   []byte
	id       int
	seq      int
	maxrtt   time.Duration
	dest     net.Addr
}

type ICMP struct {
	Addr    net.Addr
	RTT     time.Duration
	MaxRTT  time.Duration
	MinRTT  time.Duration
	AvgRTT  time.Duration
	Final   bool
	Timeout bool
	Down    bool
	Error   error
}

func PingWithArgs(ip net.IP, args ...string) (PingSt, error) {
	args = append([]string{ip.String()}, args...)
	return execute(args)
}

func execute(args []string) (PingSt, error) {
	pr := PingSt{
		SendPk: 0,
		RevcPk: 0,
		LossPk: 0,
		MinDelay: 0.0,
		MaxDelay: 0.0,
		AvgDelay: 0.0,
	}

	path, err := exec.LookPath("ping")
	if err != nil {
		return pr, err
	}

	out, err := exec.Command(path, args...).Output()
	if err != nil {
		return pr, err
	}

	ret := parseResult(string(out))
	if err != nil {
		return pr, err
	}

	return ret, nil
}

func parseResult(r string) PingSt {

	lines := strings.Split(r, "\n")

	pr := PingSt{
		SendPk: 0,
		RevcPk: 0,
		LossPk: 0,
		MinDelay: 0.0,
		MaxDelay: 0.0,
		AvgDelay: 0.0,
	}

	length := len(lines)

	for key, line := range lines {
		// ping statistics line
		if strings.HasPrefix(line, "---"){
			if key + 1 < length {
				statistics := lines[key + 1]
				l := strings.Split(statistics, ", ")
				if len(l) == 4 {
					// received
					received := l[1]
					received = strings.TrimRight(received, " received")
					pr.RevcPk, _ = strconv.Atoi(received)
				}
			}

			// rtt min/avg/max/mdev = 0.345/0.545/1.089/0.277 ms
			if key + 2 < length {
				rtt := lines[key + 2]
				if strings.HasPrefix(rtt, "rtt") {
					l := strings.Split(rtt, " = ")
					if len(l) == 2 {
						l2 := strings.Split(l[1], "/")
						if len(l2) == 4 {
							// min
							pr.MinDelay, _ = strconv.ParseFloat(l2[0], 64)
							// avg
							pr.AvgDelay, _ = strconv.ParseFloat(l2[1], 64)
							// max
							pr.MaxDelay, _ = strconv.ParseFloat(l2[2], 64)
						}
					}
				}
			}
		}
	}

	return pr
}

func (t *pkg) send(ttl int) ICMP {
	var hop ICMP
	var err error
	t.conn, hop.Error = net.ListenPacket("ip4:icmp", "0.0.0.0")
	if nil != err {
		return hop
	}
	defer t.conn.Close()
	t.ipv4conn = ipv4.NewPacketConn(t.conn)
	defer t.ipv4conn.Close()
	hop.Error = t.conn.SetReadDeadline(time.Now().Add(t.maxrtt))
	if nil != hop.Error {
		return hop
	}
	if nil != t.ipv4conn {
		hop.Error = t.ipv4conn.SetTTL(ttl)
	}
	if nil != hop.Error {
		return hop
	}
	sendOn := time.Now()
	if nil != t.ipv4conn {
		_, hop.Error = t.conn.WriteTo(t.netmsg, t.dest)
	}
	if nil != hop.Error {
		return hop
	}
	buf := make([]byte, 1500)
	for {
		var readLen int
		readLen, hop.Addr, hop.Error = t.conn.ReadFrom(buf)
		if nerr, ok := hop.Error.(net.Error); ok && nerr.Timeout() {
			hop.Timeout = true
			return hop
		}
		if nil != hop.Error {
			return hop
		}
		var result *icmp.Message
		if nil != t.ipv4conn {
			result, hop.Error = icmp.ParseMessage(1, buf[:readLen])
		}
		if nil != hop.Error {
			return hop
		}
		switch result.Type {
		case ipv4.ICMPTypeEchoReply:
			if rply, ok := result.Body.(*icmp.Echo); ok {
				if t.id == rply.ID && t.seq == rply.Seq {
					hop.Final = true
					hop.RTT = time.Since(sendOn)
					return hop
				}

			}
		case ipv4.ICMPTypeTimeExceeded:
			if rply, ok := result.Body.(*icmp.TimeExceeded); ok {
				if len(rply.Data) > 24 {
					if uint16(t.id) == binary.BigEndian.Uint16(rply.Data[24:26]) {
						hop.RTT = time.Since(sendOn)
						return hop
					}
				}
			}
		case ipv4.ICMPTypeDestinationUnreachable:
			if rply, ok := result.Body.(*icmp.Echo); ok {
				if t.id == rply.ID && t.seq == rply.Seq {
					hop.Down = true
					hop.RTT = time.Since(sendOn)
					return hop
				}

			}
		}
	}
}

func runPing(Addr string, maxrtt time.Duration, maxttl int, seq int) (float64, error) {
	var res pkg
	var err error
	res.dest, err = net.ResolveIPAddr("ip", Addr)
	if err != nil {
		return 0, err
	}
	res.maxrtt = maxrtt
	//res.id = rand.Int() % 0x7fff
	res.id = rand.Intn(65535)
	res.seq = seq
	res.msg = icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: res.id, Seq: res.seq}}
	res.netmsg, err = res.msg.Marshal(nil)
	if nil != err {
		return 0, err
	}
	pingRsult := res.send(maxttl)
	return float64(pingRsult.RTT.Nanoseconds()) / 1e6, pingRsult.Error
}

func StartPing(t string, count int) {

	stat := PingSt{}
	stat.MinDelay = -1
	lossPK := 0
	maxCount := 5
	if count > 0 {
		maxCount = count
	}
	for i := 0; i < maxCount; i++ {
		//starttime := time.Now().UnixNano()
		delay, err := runPing(t, 3 * time.Second, 254, i)
		if err == nil {
			stat.AvgDelay = stat.AvgDelay + delay
			if stat.MaxDelay < delay {
				stat.MaxDelay = delay
			}
			if stat.MinDelay == -1 || stat.MinDelay > delay {
				stat.MinDelay = delay
			}
			stat.RevcPk = stat.RevcPk + 1
		} else {
			lossPK = lossPK + 1
		}
		stat.SendPk = stat.SendPk + 1
		stat.LossPk = int((float64(lossPK) / float64(stat.SendPk)) * 100)
		//duringtime := time.Now().UnixNano() - starttime
		//time.Sleep(time.Duration(3000 * 1000000 - duringtime) * time.Nanosecond)
		time.Sleep(800 * time.Millisecond)
	}
	stat.AvgDelay = stat.AvgDelay / float64(stat.SendPk)
	cache.GetInstance().Add(t, 3 * time.Minute, stat)
}

func SystemCmdPing(ip string, count int) {
	target := net.ParseIP(ip)
	pr, err := PingWithArgs(target, fmt.Sprintf("-c %d -i 0.8", count))
	if err != nil {
		return
	}

	stat := PingSt{
		SendPk: count,
		RevcPk: pr.RevcPk,
		LossPk: count - pr.RevcPk,
		MaxDelay: pr.MaxDelay,
		MinDelay: pr.MinDelay,
		AvgDelay: pr.AvgDelay,
	}
	cache.GetInstance().Add(fmt.Sprintf("%s", ip), 3 * time.Minute, stat)
}