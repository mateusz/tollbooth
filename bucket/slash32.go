package bucket

import (
	"crypto/md5"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/mateusz/tempomat/lib/config"
)

type Slash32 struct {
	Bucket
	hash              map[string]entrySlash32
	trustedProxiesMap map[string]bool
	netmask           int
}

func NewSlash32(rate float64, trustedProxiesMap map[string]bool, netmask int, hashMaxLen int) *Slash32 {
	b := &Slash32{
		Bucket: Bucket{
			rate:       rate,
			hashMaxLen: hashMaxLen,
		},
		hash:              make(map[string]entrySlash32),
		trustedProxiesMap: trustedProxiesMap,
		netmask:           netmask,
	}
	go b.ticker()
	return b
}

func (b *Slash32) Title() string {
	return fmt.Sprintf("Slash%d", b.netmask)
}

func (b *Slash32) SetConfig(c config.Config) {
	// should be protected from race conditions?
	switch b.netmask {
	case 32:
		b.rate = c.Slash32Share
	case 24:
		b.rate = c.Slash24Share
	case 16:
		b.rate = c.Slash16Share
	}
	b.Bucket.SetConfig(c)
}

func (b *Slash32) Entries() map[string]Entry {
	b.RLock()
	entries := make(map[string]Entry)
	for k, c := range b.hash {
		entries[k] = c
	}
	b.RUnlock()
	return entries
}

func (b *Slash32) Netmask() int {
	return b.netmask
}

func (b *Slash32) Register(r *http.Request, cost float64) {
	var err error
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if _, ok := b.trustedProxiesMap[ip]; ok {
			headerIp := getIPAdressFromHeaders(r, b.trustedProxiesMap)
			if headerIp != "" {
				ip = headerIp
			}
		}
	}

	ipnet := "0.0.0.0/0"
	_, network, err := net.ParseCIDR(fmt.Sprintf("%s/%d", ip, b.netmask))
	// @todo shouldn't this error be handled properly? is it ipv6 compat?
	if err == nil {
		ipnet = network.String()
	}

	hash := md5.New()
	io.WriteString(hash, ipnet)
	key := fmt.Sprintf("%x", hash.Sum(nil))

	b.Lock()
	if c, ok := b.hash[key]; ok {
		c.credit -= cost
		b.hash[key] = c
	} else {
		b.hash[key] = entrySlash32{
			netMask: ipnet,
			credit:  b.rate*10.0 - cost,
		}
	}
	b.Unlock()
	log.Info(fmt.Sprintf("Slash%d: %s, %f billed to '%s' (%s), total is %f", b.netmask, r.URL, cost, ipnet, ip, b.hash[key].credit))
}

func (b *Slash32) DumpList() DumpList {
	b.RLock()
	defer b.RUnlock()
	return b.dumpListNoLock()
}

func (b *Slash32) dumpListNoLock() DumpList {
	l := make(DumpList, len(b.hash))
	i := 0
	for k, v := range b.hash {
		e := DumpEntry{Hash: k, Title: v.netMask, Credit: v.credit}
		l[i] = e
		i++
	}
	return l
}

func (b *Slash32) truncate(truncatedSize int) {
	newHash := make(map[string]entrySlash32)

	dumpList := b.dumpListNoLock()
	sort.Sort(CreditSortDumpList(dumpList))
	for i := 0; i < truncatedSize; i++ {
		newHash[dumpList[i].Hash] = entrySlash32{
			netMask: dumpList[i].Title,
			credit:  dumpList[i].Credit,
		}
	}
	b.hash = newHash
}

func (b *Slash32) ticker() {
	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		b.Lock()
		for k, c := range b.hash {
			// remove entries that are at their max credits
			if c.credit+b.rate > b.rate*10.0 {
				// Purge entries that are at their max credit.
				delete(b.hash, k)
				// else we give them some credits
			} else {
				c.credit += b.rate
				b.hash[k] = c
			}
		}
		// truncate some entries to not blow out the memory
		if len(b.hash) > b.hashMaxLen {
			b.truncate(b.hashMaxLen)
		}
		b.Unlock()
	}
}

type entrySlash32 struct {
	netMask string
	credit  float64
}

func (e entrySlash32) Credits() float64 {
	return e.credit
}

func (e entrySlash32) String() string {
	return fmt.Sprintf("%s,%.3f", e.netMask, e.credit)
}
