package bucket

import (
	"crypto/md5"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"time"

	"golang.org/x/time/rate"

	"github.com/mateusz/tempomat/lib/config"
)

type Slash32 struct {
	Bucket
	hash              map[string]EntrySlash32
	trustedProxiesMap map[string]bool
	netmask           int
}

func NewSlash32(c config.Config, netmask int) *Slash32 {
	b := &Slash32{
		hash:              make(map[string]EntrySlash32),
		netmask:           netmask,
	}
	b.SetConfig(c)
	go b.ticker()
	return b
}

func (b *Slash32) SetConfig(c config.Config) {
	b.Lock()
	switch b.netmask {
	case 32:
		b.rate = c.Slash32CPUs
	case 24:
		b.rate = c.Slash24CPUs
	case 16:
		b.rate = c.Slash16CPUs
	}
	b.trustedProxiesMap = c.TrustedProxiesMap
	b.truncate(0)
	b.Unlock()

	b.Bucket.SetConfig(c)
}

func (b *Slash32) String() string {
	b.RLock()
	defer b.RUnlock()
	return fmt.Sprintf("Slash%d", b.netmask)
}

func (b *Slash32) Netmask() int {
	b.RLock()
	defer b.RUnlock()
	return b.netmask
}

func (b *Slash32) Entries() Entries {
	b.RLock()
	defer b.RUnlock()

	return b.entries()
}

func (b *Slash32) entries() Entries {
	l := make(Entries, len(b.hash))
	i := 0
	for _, v := range b.hash {
		l[i] = v
		i++
	}
	return l
}

func (b *Slash32) ReserveN(r *http.Request, start time.Time, qty float64) (delay time.Duration, ok bool) {
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

	b.Lock()
	defer b.Unlock()
	entry := EntrySlash32{
		netmask: ipnet,
	}
	key := entry.Hash()

	if _, ok := b.hash[key]; ok {
		entry = b.hash[key];
	} else {
		entry.limiter = rate.NewLimiter(rate.Limit(b.rate * 1000), 30 * 1000)
	}

	rsv := entry.limiter.ReserveN(start, int(qty * 1000))
	if rsv.OK() && rsv.Delay()!=rate.InfDuration {
		ok = true
		delay = rsv.Delay()
	} else {
		ok = false
		delay = 120 * time.Second
	}

	var delayRemaining time.Duration
	elapsed := time.Now().Sub(start)
	if elapsed<=delay {
		delayRemaining = delay-elapsed
	}

	sincePrev := time.Now().Sub(entry.lastUsed)
	if sincePrev>0 && sincePrev<time.Minute {
		entry.avgSincePrev -= entry.avgSincePrev / 10
		entry.avgSincePrev += sincePrev / 10
	}

	entry.lastUsed = time.Now()
	entry.avgWait -= entry.avgWait/10
	entry.avgWait += delayRemaining / 10

	cpuSecsPerSec := qty/float64(entry.avgSincePrev.Seconds())
	if cpuSecsPerSec<100.0 {
		entry.avgCpuSecs -= entry.avgCpuSecs / 10
		entry.avgCpuSecs += cpuSecsPerSec / 10
	}

	b.hash[key] = entry

	return
}

// Not concurrency safe.
func (b *Slash32) truncate(truncatedSize int) {
	entries := b.entries()

	sort.Sort(LastUsedSortEntries(entries))
	purged := make(Entries, 0, len(entries))
	for i := 0; i < len(entries); i++ {
		if time.Now().Sub(entries[i].LastUsed())<60*time.Second {
			purged = append(purged, entries[i])
		}
	}

	sort.Sort(AvgWaitSortEntries(purged))
	newHash := make(map[string]EntrySlash32)
	for i := 0; i < truncatedSize && i<len(purged); i++ {
		newHash[purged[i].Hash()] = purged[i].(EntrySlash32)
	}

	// Note: this will overwrite recently added entries
	b.hash = newHash
}

func (b *Slash32) ticker() {
	ticker := time.NewTicker(time.Minute)
	for range ticker.C {
		b.Lock()
		b.truncate(b.hashMaxLen)
		b.Unlock()
	}
}

type EntrySlash32 struct {
	netmask      string
	lastUsed     time.Time
	avgWait      time.Duration
	avgSincePrev time.Duration
	avgCpuSecs   float64
	limiter      *rate.Limiter
}

func (e EntrySlash32) Hash() string {
	hasher := md5.New()
	io.WriteString(hasher, e.netmask)
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

func (e EntrySlash32) LastUsed() time.Time {
	return e.lastUsed
}

func (e EntrySlash32) AvgWait() time.Duration {
	return e.avgWait
}

func (e EntrySlash32) AvgSincePrev() time.Duration {
	return e.avgSincePrev
}

func (e EntrySlash32) AvgCpuSecs() float64 {
	return e.avgCpuSecs
}

func (e EntrySlash32) String() string {
	return fmt.Sprintf("%s, used %d ago", e.netmask, time.Now().Sub(e.lastUsed).Seconds())
}

func (e EntrySlash32) Title() string {
	return e.netmask
}
