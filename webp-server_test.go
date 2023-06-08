package main

import (
	"net"
	"os"
	"runtime"
	"testing"
	"time"
	"encoding/json"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

// due to test limit, we can't test for cli param part.

func TestLoadConfig(t *testing.T) {
	c := loadConfig("./config.json")
	assert.Equal(t, "./exhaust", c.ExhaustPath)
	assert.Equal(t, "127.0.0.1", c.Host)
	assert.Equal(t, "3333", c.Port)
	assert.Equal(t, 80, c.Quality)
	assert.Equal(t, "./pics", c.ImgPath)
	assert.Equal(t, []string{"jpg", "png", "jpeg", "bmp"}, c.AllowedTypes)
}

func TestDeferInit(t *testing.T) {
	// test initial value
	assert.Equal(t, "", configPath)
	assert.False(t, prefetch)
	assert.Equal(t, false, dumpSystemd)
	assert.Equal(t, false, dumpConfig)
	assert.False(t, verboseMode)
	assert.Equal(t, false, lazyMode)
}

func TestMainFunction(t *testing.T) {
	// first test verbose mode
	assert.False(t, verboseMode)
	assert.Equal(t, log.GetLevel(), log.InfoLevel)
	os.Args = []string{os.Args[0], "-v", "-prefetch"}

	// run main function
	go main()
	time.Sleep(time.Second * 5)
	// verbose, prefetch
	assert.Equal(t, log.GetLevel(), log.DebugLevel)
	assert.True(t, verboseMode)
	assert.True(t, prefetch)
	assert.False(t, lazyMode)

	// test read config value
	assert.Equal(t, "config.json", configPath)
	assert.Equal(t, runtime.NumCPU(), jobs)
	assert.Equal(t, false, dumpSystemd)
	assert.Equal(t, false, dumpConfig)

	// test port
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", "3333"), time.Second*2)
	assert.Nil(t, err)
	assert.NotNil(t, conn)
}

func newConfig() Config {
	return Config{
		Host: "127.0.0.1",
		Port: "3333",
		Quality: 80,
		ImgPath: "./pics",
		ExhaustPath: "./exhaust",
		AllowedTypes: []string{"jpg","png","jpeg","bmp"},
		EnableAVIF: false,
		EnableExtraParams: false,
	}
}

func TestMainFunctionLazy(t *testing.T) {
	var testPort = "3334"

	f, _ := os.CreateTemp("", "TestMainFunctionLazy-*.conf")
	defer os.Remove(f.Name())

	c := newConfig()
	c.Port = testPort
	cJSON, _ := json.MarshalIndent(c, "", "\t")
	f.Write(cJSON)
	f.Close()
	log.Infof("Config file written to %s",f.Name())
	os.Args = []string{os.Args[0], "-config", f.Name(), "-v", "-prefetch", "-lazy"}

	// run main function
	go main()
	time.Sleep(time.Second * 5)
	// verbose, prefetch
	assert.Equal(t, log.GetLevel(), log.DebugLevel)
	assert.True(t, verboseMode)
	assert.True(t, prefetch)
	assert.True(t, lazyMode)

	// test read config value
	assert.Equal(t, f.Name(), configPath)
	assert.Equal(t, runtime.NumCPU(), jobs)
	assert.Equal(t, runtime.NumCPU(), maxDefaultJobs)
	assert.Equal(t, runtime.NumCPU(), maxHeavyJobs)
	assert.Equal(t, false, dumpSystemd)
	assert.Equal(t, false, dumpConfig)

	// test port
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", testPort), time.Second*2)
	assert.Nil(t, err)
	assert.NotNil(t, conn)

	// test queues
	assert.NotNil(t, DefaultWorkQueue)
	assert.NotNil(t, HeavyWorkQueue)

	// test worker pools
	assert.NotNil(t, DefaultWorkerPool)
	assert.NotNil(t, HeavyWorkerPool)
	assert.False(t, DefaultWorkerPool.Stopped())
	assert.False(t, HeavyWorkerPool.Stopped())

	// test sets
	assert.NotNil(t, WorkOngoingSet)

	// test scheduler
	assert.NotNil(t, Beat)
	assert.True(t, Beat.IsRunning())
}

func TestProxySwitch(t *testing.T) {
	// real proxy mode
	assert.False(t, proxyMode)
	config.ImgPath = "https://z.cn"
	switchProxyMode()
	assert.True(t, proxyMode)

	// normal
	config.ImgPath = os.TempDir()
	switchProxyMode()
	assert.False(t, proxyMode)
}
