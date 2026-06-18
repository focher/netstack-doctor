package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ---- Interface enumeration (Layer 1 / 2) ----

type IfaceInfo struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac"`
	MTU   int      `json:"mtu"`
	Up    bool     `json:"up"`
	Loop  bool     `json:"loopback"`
	Addrs []string `json:"addrs"`
}

func interfaceSummary() []IfaceInfo {
	var out []IfaceInfo
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		info := IfaceInfo{
			Name: ifc.Name,
			MAC:  ifc.HardwareAddr.String(),
			MTU:  ifc.MTU,
			Up:   ifc.Flags&net.FlagUp != 0,
			Loop: ifc.Flags&net.FlagLoopback != 0,
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			info.Addrs = append(info.Addrs, a.String())
		}
		out = append(out, info)
	}
	return out
}

// localAddrs returns local unicast IPs split by family.
func localAddrs() (v4 []string, v6 []string) {
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.To4() != nil {
				v4 = append(v4, ip.String())
			} else if ip.To16() != nil {
				v6 = append(v6, ip.String())
			}
		}
	}
	return
}

func hasGlobalIPv6() bool {
	_, v6 := localAddrs()
	return len(v6) > 0
}

// ---- Default gateway (Layer 3) ----

// defaultGateway returns the IPv4 default gateway address.
func defaultGateway() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := runCmd(3*time.Second, "route", "-n", "get", "default")
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "gateway:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "gateway:")), nil
			}
		}
	case "windows":
		out, err := runCmd(5*time.Second, "route", "print", "0.0.0.0")
		if err != nil {
			return "", err
		}
		re := regexp.MustCompile(`0\.0\.0\.0\s+0\.0\.0\.0\s+(\d+\.\d+\.\d+\.\d+)`)
		if m := re.FindStringSubmatch(out); m != nil {
			return m[1], nil
		}
	default: // linux
		out, err := runCmd(3*time.Second, "ip", "route", "show", "default")
		if err == nil {
			f := strings.Fields(out)
			for i, t := range f {
				if t == "via" && i+1 < len(f) {
					return f[i+1], nil
				}
			}
		}
	}
	return "", fmt.Errorf("default gateway not found")
}

// gatewayMethod describes the OS-specific command used to find the default route.
func gatewayMethod() string {
	switch runtime.GOOS {
	case "darwin":
		return "route -n get default"
	case "windows":
		return "route print 0.0.0.0"
	default:
		return "ip route show default"
	}
}

// arpLookup returns the MAC for an IP from the system ARP/neighbor table.
func arpLookup(ip string) (string, error) {
	out, err := runCmd(3*time.Second, "arp", "-n", ip)
	if err != nil {
		// Windows arp wants -a
		out, err = runCmd(3*time.Second, "arp", "-a", ip)
		if err != nil {
			return "", err
		}
	}
	re := regexp.MustCompile(`([0-9a-fA-F]{1,2}[:-]){5}[0-9a-fA-F]{1,2}`)
	if m := re.FindString(out); m != "" {
		return strings.ToLower(strings.ReplaceAll(m, "-", ":")), nil
	}
	return "", fmt.Errorf("no ARP entry for %s", ip)
}

// ---- ICMP ping via system tool (Layer 3) ----

type PingResult struct {
	Target   string
	OK       bool
	AvgMs    float64
	Loss     string
	Raw      string
	Err      string
	Cmd      string
	ExitInfo string
}

func ping(target string, ipv6 bool, count int) PingResult {
	res := PingResult{Target: target}
	bin := "ping"
	var args []string
	switch runtime.GOOS {
	case "windows":
		fam := "-4"
		if ipv6 {
			fam = "-6"
		}
		args = []string{fam, "-n", fmt.Sprintf("%d", count), "-w", "1500", target}
	case "darwin":
		if ipv6 {
			bin = "ping6"
		}
		args = []string{"-c", fmt.Sprintf("%d", count), "-t", "5", target}
	default:
		fam := "-4"
		if ipv6 {
			fam = "-6"
		}
		args = []string{fam, "-c", fmt.Sprintf("%d", count), "-W", "2", target}
	}
	res.Cmd = bin + " " + strings.Join(args, " ")
	out, err := runCmd(time.Duration(count+5)*time.Second, bin, args...)
	return parsePing(res, out, err)
}

func parsePing(res PingResult, out string, err error) PingResult {
	res.Raw = strings.TrimSpace(out)
	if err != nil {
		res.Err = err.Error()
		res.ExitInfo = err.Error()
	} else {
		res.ExitInfo = "exit status 0"
	}
	// average latency
	reAvg := regexp.MustCompile(`(?:=|Average =)\s*[\d.]+/([\d.]+)/|Average = (\d+)ms`)
	if m := reAvg.FindStringSubmatch(out); m != nil {
		for _, g := range m[1:] {
			if g != "" {
				fmt.Sscanf(g, "%f", &res.AvgMs)
			}
		}
	}
	// loss
	reLoss := regexp.MustCompile(`([\d.]+)% (?:packet )?loss`)
	if m := reLoss.FindStringSubmatch(out); m != nil {
		res.Loss = m[1] + "%"
		res.OK = m[1] != "100" && m[1] != "100.0"
	} else if err == nil {
		res.OK = strings.Contains(out, "ttl=") || strings.Contains(out, "TTL=")
	}
	return res
}

// ---- traceroute (Layer 3) ----

func traceroute(target string, ipv6 bool, maxHops int) (raw string, hops int, cmd string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "windows":
		name = "tracert"
		fam := "-4"
		if ipv6 {
			fam = "-6"
		}
		args = []string{fam, "-d", "-h", fmt.Sprintf("%d", maxHops), "-w", "1000", target}
	case "darwin":
		name = "traceroute"
		if ipv6 {
			name = "traceroute6"
		}
		args = []string{"-n", "-w", "1", "-q", "1", "-m", fmt.Sprintf("%d", maxHops), target}
	default:
		name = "traceroute"
		fam := "-4"
		if ipv6 {
			fam = "-6"
		}
		args = []string{fam, "-n", "-w", "1", "-q", "1", "-m", fmt.Sprintf("%d", maxHops), target}
	}
	cmd = name + " " + strings.Join(args, " ")
	out, _ := runCmd(time.Duration(maxHops+5)*time.Second, name, args...)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if regexp.MustCompile(`^\d+`).MatchString(line) {
			hops++
		}
	}
	return strings.TrimSpace(out), hops, cmd
}

// ---- helpers ----

func runCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
