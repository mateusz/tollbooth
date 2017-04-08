package bucket

import (
	"github.com/mateusz/tempomat/lib/config"
	"net/http"
	"strings"
	"sync"
)

type Bucket struct {
	lowCreditThreshold float64
	rate               float64
	hashMaxLen         int
	sync.RWMutex
}

func (b *Bucket) SetConfig(c config.Config) {
	b.lowCreditThreshold = c.LowCreditThreshold
	b.hashMaxLen = c.HashMaxLen
}

func (b *Bucket) Threshold() float64 {
	b.RLock()
	defer b.RUnlock()
	return b.lowCreditThreshold
}

func getIPAdressFromHeaders(r *http.Request, m map[string]bool) string {
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip"} {
		header := r.Header.Get(h)
		if header == "" {
			continue
		}

		addresses := strings.Split(header, ",")
		ip := ""
		for i := len(addresses) - 1; i >= 0; i-- {
			ip = strings.TrimSpace(addresses[i])
			if _, ok := m[ip]; !ok {
				break
			}
		}
		return ip
	}
	return ""
}
