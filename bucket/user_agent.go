package bucket

import (
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/mateusz/tempomat/lib/config"
)

type UserAgent struct {
	Bucket
	hash map[string]entryUserAgent
}

func NewUserAgent(rate float64, hashMaxLen int) *UserAgent {
	b := &UserAgent{
		Bucket: Bucket{
			rate:       rate,
			hashMaxLen: hashMaxLen,
		},
		hash: make(map[string]entryUserAgent),
	}
	go b.ticker()
	return b
}

func (b *UserAgent) Title() string {
	return "UserAgent"
}

func (b *UserAgent) SetConfig(c config.Config) {
	// should be protected from race conditions?
	b.rate = c.UserAgentShare
	b.Bucket.SetConfig(c)
}

func (b *UserAgent) Entries() map[string]Entry {
	b.RLock()
	entries := make(map[string]Entry)
	for k, c := range b.hash {
		entries[k] = c
	}
	b.RUnlock()
	return entries
}

func (b *UserAgent) Register(r *http.Request, cost float64) {
	ua := r.UserAgent()

	hash := md5.New()
	io.WriteString(hash, ua)
	key := fmt.Sprintf("%x", hash.Sum(nil))

	b.Lock()
	// subtract the credits since this it's already in ther
	if c, ok := b.hash[key]; ok {
		c.credit -= cost
		b.hash[key] = c
		// new entry, give it full credits - 1
	} else {
		b.hash[key] = entryUserAgent{
			userAgent: ua,
			credit:    b.rate*10.0 - cost,
		}
	}

	log.Info(fmt.Sprintf("UserAgent: %s, %f billed to '%s', total is %f", r.URL, cost, ua, b.hash[key].credit))
	b.Unlock()
}

func (b *UserAgent) DumpList() DumpList {
	b.RLock()
	defer b.RUnlock()
	return b.dumpListNoLock()
}

func (b *UserAgent) dumpListNoLock() DumpList {
	l := make(DumpList, len(b.hash))
	i := 0
	for _, v := range b.hash {
		e := DumpEntry{Title: v.userAgent, Credit: v.credit}
		l[i] = e
		i++
	}
	return l
}

// this operation is not concurrency-safe by itself
func (b *UserAgent) truncate(truncatedSize int) {
	newHash := make(map[string]entryUserAgent)

	dumpList := b.dumpListNoLock()
	sort.Sort(CreditSortDumpList(dumpList))
	for i := 0; i < truncatedSize; i++ {
		newHash[dumpList[i].Hash] = entryUserAgent{
			userAgent: dumpList[i].Title,
			credit:    dumpList[i].Credit,
		}
	}
	b.hash = newHash
}

func (b *UserAgent) ticker() {
	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		b.Lock()
		for k, c := range b.hash {
			// remove entries that are at their max credits
			if c.credit+b.rate > b.rate*10.0 {
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

type entryUserAgent struct {
	userAgent string
	credit    float64
}

func (e entryUserAgent) Credits() float64 {
	return e.credit
}

func (e entryUserAgent) String() string {
	return fmt.Sprintf("%s,%.3f", e.userAgent, e.credit)
}
