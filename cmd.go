package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"
	//"net/rpc"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/mateusz/tempomat/api"
	"github.com/mateusz/tempomat/bucket"
	"github.com/mateusz/tempomat/lib/config"
	"github.com/rifflock/lfshook"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/load"
	_ "net/http/pprof"
	"net/rpc"
)

var conf config.Config

var buckets []bucket.Bucketable

var statsLog *log.Logger
var cpuCount float64

func utilisation() (float64, error) {
	// beware that the variable name load collides with the package load
	load, err := load.Avg()
	if err != nil {
		return -1, err
	}

	return load.Load1 / cpuCount, nil
}

func init() {
	log.SetOutput(os.Stderr)
	log.SetLevel(log.WarnLevel)

	conf = config.New()

	jsonStr, err := ioutil.ReadFile("/etc/tempomat.json")
	if err != nil {
		log.Error(err)
		log.Fatal("Refusing to start on unreadable config file.")
	}
	if err = json.Unmarshal(jsonStr, &conf); err != nil {
		log.Error(err)
		log.Fatal("Refusing to start on unparseable config file.")
	}

	proxies := strings.Split(conf.TrustedProxies, ",")
	for _, proxy := range proxies {
		conf.TrustedProxiesMap[proxy] = true
	}

	if conf.LogFile != "" {
		log.AddHook(lfshook.NewHook(lfshook.PathMap{
			log.PanicLevel: conf.LogFile,
			log.FatalLevel: conf.LogFile,
			log.ErrorLevel: conf.LogFile,
			log.WarnLevel:  conf.LogFile,
			log.InfoLevel:  conf.LogFile,
			log.DebugLevel: conf.LogFile,
		}))
	}

	statsLog = log.New()
	statsLog.Out = ioutil.Discard
	statsLog.Level = log.InfoLevel
	statsLog.Formatter = &StatsFormatter{}
	if conf.StatsFile != "" {
		var err error
		statsLog.Out, err = os.OpenFile(conf.StatsFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			log.Fatal(err)
		}
	}

	if conf.Debug {
		log.SetLevel(log.DebugLevel)
		statsLog.Level = log.DebugLevel

		conf.Print()
	}

	cpuCountInt, err := cpu.Counts(true)
	if err != nil {
		log.Fatal(err)
	}
	cpuCount = float64(cpuCountInt)

	if conf.Graphite != "" {
		if conf.GraphitePrefix == "" {
			log.Fatal("Configuration failure: 'graphitePrefix' is required if 'graphite' is specified")
		}
		var err error
		conf.GraphiteURL, err = url.Parse(conf.Graphite)
		if err != nil {
			log.Fatal(err)
		}
	}

	buckets = append(buckets, bucket.NewSlash32(cpuCount*conf.Slash32Share, conf.TrustedProxiesMap, 32, conf.HashMaxLen))
	buckets = append(buckets, bucket.NewSlash32(cpuCount*conf.Slash24Share, conf.TrustedProxiesMap, 24, conf.HashMaxLen))
	buckets = append(buckets, bucket.NewSlash32(cpuCount*conf.Slash16Share, conf.TrustedProxiesMap, 16, conf.HashMaxLen))
	buckets = append(buckets, bucket.NewUserAgent(cpuCount*conf.UserAgentShare, conf.HashMaxLen))
}

func sendMetric(metric string, value string) {
	dialer, err := net.Dial("tcp", conf.GraphiteURL.Host)
	if err != nil {
		log.Warn(fmt.Sprintf("Failed to connect to graphite server: %s", err))
		return
	}

	_, err = dialer.Write([]byte(fmt.Sprintf("%s.%s %s %d\n", conf.GraphitePrefix, metric, value, time.Now().Unix())))
	if err != nil {
		log.Warn(fmt.Sprintf("Could not write to graphite server: %s", err))
	}
	if err = dialer.Close(); err != nil {
		log.Warn(fmt.Sprintf("Could Close() connection to graphite server: %s", err))
	}
}

func middleware(proxy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		proxy.ServeHTTP(w, r)
		reqTime := time.Since(start)

		u, err := utilisation()
		if err != nil {
			log.Warn(fmt.Sprintf("%s, assuming utilisation=1.0", err))
			u = 1.0
		}

		// Cost is expressed in the amount of compute seconds consumed.
		// It's scaled down by load average if the CPU is 100% saturated to take CPU contention into account.
		cost := float64(reqTime) / 1e9
		if u > 1.0 {
			cost = cost / u
		}

		for _, b := range buckets {
			// be very very careful of not reading the request.Body unless copying it before.
			// The Body is a io.ReadClose so if you read it, it will be empty (closed) for other calls to it,
			// @see https://medium.com/@xoen/golang-read-from-an-io-readwriter-without-loosing-its-content-2c6911805361
			// I'm not sure how this works with the reverseProxy functionality
			// wouldn't it be great if we could mark stuff as immutable..
			b.Register(r, cost)
		}
	})
}

func statsLogger() {
	ticker := time.NewTicker(time.Minute)
	for range ticker.C {
		for _, b := range buckets {
			Dump(b, statsLog)
			sendMetric(b.Title(), fmt.Sprintf("%d", CountUnderThreshold(b)))

		}
	}
}

type BucketDumper struct {
	buckets []bucket.Bucketable
}

func (bd *BucketDumper) Slash32(args *api.EmptyArgs, reply *bucket.DumpList) error {
	for _, b := range bd.buckets {
		if slash := b.(*bucket.Slash32); slash.Netmask() == 32 {
			*reply = slash.DumpList()
		}
	}
	return nil
}

func (bd *BucketDumper) Slash24(args *api.EmptyArgs, reply *bucket.DumpList) error {
	for _, b := range bd.buckets {
		if slash := b.(*bucket.Slash32); slash.Netmask() == 24 {
			*reply = slash.DumpList()
		}
	}
	return nil
}

func (bd *BucketDumper) Slash16(args *api.EmptyArgs, reply *bucket.DumpList) error {
	for _, b := range bd.buckets {
		if slash := b.(*bucket.Slash32); slash.Netmask() == 16 {
			*reply = slash.DumpList()
		}
	}
	return nil
}

func (bd *BucketDumper) UserAgent(args *api.EmptyArgs, reply *bucket.DumpList) error {
	for _, b := range bd.buckets {
		if ua := b.(*bucket.UserAgent); ua != nil {
			*reply = ua.DumpList()
		}
	}
	return nil
}

func listen() {
	// beware that url is colliding with the imported package url
	url, err := url.Parse(conf.Backend)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(url)
	handler := middleware(proxy)
	http.ListenAndServe(fmt.Sprintf(":%d", conf.ListenPort), handler)
}

func sighupHandler() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	for {
		<-c
		log.Warn("SIGHUP received, reloading config")

		newConfig := config.New()
		jsonStr, err := ioutil.ReadFile("/etc/tempomat.json")
		if err != nil {
			log.Error(err)
			log.Error("Refusing to reload on unreadable config file.")
		}
		if err = json.Unmarshal(jsonStr, &newConfig); err != nil {
			log.Error(err)
			log.Error("Refusing to reload on unparseable config file.")
		}

		// let the buckets pick and choose from the config
		for _, b := range buckets {
			b.SetConfig(conf)
		}

		// maybe using an config.Config.Update(config.Config) method instead?
		conf.UserAgentShare = newConfig.UserAgentShare
		conf.Slash32Share = newConfig.Slash32Share
		conf.Slash24Share = newConfig.Slash24Share
		conf.Slash16Share = newConfig.Slash16Share

		if conf.Debug {
			conf.Print()
		}
	}
}

func main() {
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	go sighupHandler()
	go statsLogger()

	// @todo: need to figure this out
	bd := &BucketDumper{
		buckets: buckets,
	}
	rpc.Register(bd)
	rpc.HandleHTTP()
	l, e := net.Listen("tcp", ":29999")
	if e != nil {
		log.Fatal("Unable to set up RPC listener:", e)
	}
	go http.Serve(l, nil)

	listen()
}

func CountUnderThreshold(b bucket.Bucketable) int {
	var count int
	for _, e := range b.Entries() {
		if e.Credits()-b.Threshold() < 0 {
			count++
		}
	}
	return count
}

func Dump(b bucket.Bucketable, l *log.Logger) {
	for k, e := range b.Entries() {
		if e.Credits()-b.Threshold() < 0 {
			l.Info(fmt.Sprintf("%s,%s,'%s'", b.Title(), k, e))
		}
	}
}
