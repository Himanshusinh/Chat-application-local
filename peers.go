package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

const onlineWindow = 15 * time.Second

type PeerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Avatar  string `json:"avatar"`
	OS      string `json:"os"`
	Port    int    `json:"port"`
	Version string `json:"version"`
}

type Peer struct {
	PeerInfo
	Addr     string    `json:"addr"` // host:port of the peer's P2P server
	Online   bool      `json:"online"`
	Via      string    `json:"via"` // "lan", "direct" or "gossip"
	LastSeen time.Time `json:"lastSeen"`
	lastLAN  time.Time
}

type PeerTable struct {
	mu   sync.Mutex
	m    map[string]*Peer
	path string
}

var peers *PeerTable

func newPeerTable(path string) *PeerTable {
	t := &PeerTable{m: map[string]*Peer{}, path: path}
	if b, err := os.ReadFile(path); err == nil {
		var list []*Peer
		if json.Unmarshal(b, &list) == nil {
			for _, p := range list {
				if p.ID != "" {
					p.Online = false
					t.m[p.ID] = p
				}
			}
		}
	}
	return t
}

func (t *PeerTable) save() {
	b, _ := json.MarshalIndent(t.list(), "", "  ")
	writeFileAtomic(t.path, b)
}

// seen records that we heard from a peer at ip. Returns true when the peer is
// new or just came online.
func (t *PeerTable) seen(info PeerInfo, ip, via string) bool {
	if info.ID == "" || info.ID == getCfg().ID || info.Port == 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[info.ID]
	if !ok {
		p = &Peer{}
		t.m[info.ID] = p
	}
	wasOnline := p.Online
	changed := !ok || p.Name != info.Name || p.Avatar != info.Avatar
	p.PeerInfo = info
	p.Addr = net.JoinHostPort(ip, strconv.Itoa(info.Port))
	p.LastSeen = time.Now()
	p.Online = true
	p.Via = via
	if via == "lan" {
		p.lastLAN = p.LastSeen
	}
	if changed || !wasOnline {
		go emitPeers()
		if !wasOnline {
			go flushPending(info.ID)
		}
	}
	return !wasOnline
}

// learn adds a peer we only heard about from someone else (gossip).
func (t *PeerTable) learn(p Peer) {
	if p.ID == "" || p.ID == getCfg().ID || p.Addr == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.m[p.ID]; ok {
		return
	}
	p.Online, p.Via, p.LastSeen = false, "gossip", time.Time{}
	t.m[p.ID] = &p
}

func (t *PeerTable) markOffline(id string) {
	t.mu.Lock()
	if p, ok := t.m[id]; ok && p.Online {
		p.Online = false
		p.LastSeen = time.Now().Add(-onlineWindow)
		go emitPeers()
	}
	t.mu.Unlock()
}

func (t *PeerTable) get(id string) (Peer, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[id]
	if !ok {
		return Peer{}, false
	}
	return *p, true
}

func (t *PeerTable) list() []Peer {
	t.mu.Lock()
	out := make([]Peer, 0, len(t.m))
	for _, p := range t.m {
		out = append(out, *p)
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Online != out[j].Online {
			return out[i].Online
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (t *PeerTable) online() []Peer {
	var out []Peer
	for _, p := range t.list() {
		if p.Online {
			out = append(out, p)
		}
	}
	return out
}

func emitPeers() { hub.broadcast(map[string]any{"type": "peers", "peers": peers.list()}) }

// runPeerWatcher flips peers to offline when we stop hearing from them.
func runPeerWatcher() {
	tick := time.NewTicker(3 * time.Second)
	saveTick := 0
	for range tick.C {
		changed := false
		peers.mu.Lock()
		for _, p := range peers.m {
			if p.Online && time.Since(p.LastSeen) > onlineWindow {
				p.Online = false
				changed = true
			}
		}
		peers.mu.Unlock()
		if changed {
			emitPeers()
		}
		if saveTick++; saveTick%10 == 0 {
			peers.save()
		}
	}
}

// ---------------------------------------------------------------- discovery

type announce struct {
	App  string `json:"app"`
	Team string `json:"team"`
	Bye  bool   `json:"bye,omitempty"`
	PeerInfo
}

var udpConn *net.UDPConn

func runDiscovery() {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: discoveryPort})
	if err != nil {
		logger.Printf("discovery disabled (UDP %d): %v", discoveryPort, err)
		return
	}
	udpConn = conn
	go func() {
		buf := make([]byte, 4096)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				logger.Printf("discovery read: %v", err)
				return
			}
			var a announce
			if json.Unmarshal(buf[:n], &a) != nil || a.App != "officechat" || a.Team != teamHash() || a.ID == getCfg().ID {
				continue
			}
			if a.Bye {
				peers.markOffline(a.ID)
				continue
			}
			if peers.seen(a.PeerInfo, src.IP.String(), "lan") {
				// Answer directly so the newcomer sees us immediately.
				sendAnnounce(false, &net.UDPAddr{IP: src.IP, Port: discoveryPort})
			}
		}
	}()
	for {
		sendAnnounce(false, nil)
		time.Sleep(3 * time.Second)
	}
}

func sendBye() { sendAnnounce(true, nil) }

func sendAnnounce(bye bool, to *net.UDPAddr) {
	if udpConn == nil {
		return
	}
	b, _ := json.Marshal(announce{App: "officechat", Team: teamHash(), Bye: bye, PeerInfo: myInfo()})
	if to != nil {
		_, _ = udpConn.WriteToUDP(b, to)
		return
	}
	for _, ip := range broadcastAddrs() {
		_, _ = udpConn.WriteToUDP(b, &net.UDPAddr{IP: ip, Port: discoveryPort})
	}
}

// broadcastAddrs returns 255.255.255.255 plus the directed broadcast address
// of every active IPv4 interface. macOS only sends the limited broadcast out
// of the primary interface, so the directed ones matter there.
func broadcastAddrs() []net.IP {
	out := []net.IP{net.IPv4bcast}
	seen := map[string]bool{net.IPv4bcast.String(): true}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || len(ipn.Mask) != 4 {
				continue
			}
			b := make(net.IP, 4)
			for i := range b {
				b[i] = ip4[i] | ^ipn.Mask[i]
			}
			if !seen[b.String()] {
				seen[b.String()] = true
				out = append(out, b)
			}
		}
	}
	return out
}

// localIPs lists this computer's addresses so people can add it by hand.
func localIPs() []string {
	var out []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}

// ------------------------------------------------------------ direct pinger

type helloResp struct {
	Me    PeerInfo `json:"me"`
	Peers []Peer   `json:"peers"`
}

// runPinger reaches peers that broadcast can't: manually added addresses
// (other subnets, VPN, Tailscale, the internet) and anyone we learned about
// through gossip. Contacting one remote office computer is enough to learn
// about everyone it knows.
func runPinger() {
	for {
		targets := map[string]bool{}
		for _, a := range getCfg().ManualPeers {
			targets[normalizeAddr(a)] = true
		}
		for _, p := range peers.list() {
			if time.Since(p.lastLAN) > 7*time.Second && p.Addr != "" {
				targets[p.Addr] = true
			}
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, 16)
		for addr := range targets {
			wg.Add(1)
			sem <- struct{}{}
			go func(a string) {
				defer func() { <-sem; wg.Done() }()
				_, _ = hello(a)
			}(addr)
		}
		wg.Wait()
		time.Sleep(5 * time.Second)
	}
}

// announceNow pushes our current name/picture to everyone right away
// (instead of waiting for the next broadcast round).
func announceNow() {
	go sendAnnounce(false, nil)
	for _, p := range peers.online() {
		go hello(p.Addr)
	}
}

func normalizeAddr(a string) string {
	if _, _, err := net.SplitHostPort(a); err != nil {
		return net.JoinHostPort(a, strconv.Itoa(defaultP2PPort))
	}
	return a
}

func hello(addr string) (PeerInfo, error) {
	body, _ := json.Marshal(myInfo())
	req, _ := http.NewRequest("POST", "http://"+addr+"/p2p/hello", bytes.NewReader(body))
	setP2PHeaders(req)
	resp, err := ctlClient.Do(req)
	if err != nil {
		return PeerInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return PeerInfo{}, httpErr(resp)
	}
	var hr helloResp
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		return PeerInfo{}, err
	}
	host, _, _ := net.SplitHostPort(addr)
	via := "direct"
	if p, ok := peers.get(hr.Me.ID); ok && time.Since(p.lastLAN) < 7*time.Second {
		via = "lan"
	}
	peers.seen(hr.Me, host, via)
	for _, p := range hr.Peers {
		peers.learn(p)
	}
	return hr.Me, nil
}
