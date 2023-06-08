package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"time"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/etag"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/patrickmn/go-cache"

	pq "github.com/emirpasic/gods/queues/priorityqueue"
	hs "github.com/emirpasic/gods/sets/hashset"
	"github.com/emirpasic/gods/utils"
	pond "github.com/alitto/pond"
	"github.com/go-co-op/gocron"

	log "github.com/sirupsen/logrus"
)

var WriteLock *cache.Cache
var DefaultWorkerPool *pond.WorkerPool // Default worker pool
var HeavyWorkerPool *pond.WorkerPool   // Worker pool for heavy/long convertions (e.g. Avif)
var VipsConfig *vips.Config
var Beat *gocron.Scheduler

// Element is an entry in the priority queue
type Element struct {
	itype       string
	raw         string
	optimized   string
	quality     int
	extraParams ExtraParams
	priority    int
}

// Comparator function (sort by element's priority value in descending order)
func byPriority(a, b interface{}) int {
	priorityA := a.(Element).priority
	priorityB := b.(Element).priority
	return -utils.IntComparator(priorityA, priorityB) // "-" descending order
}

func loadConfig(path string) Config {
	jsonObject, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	decoder := json.NewDecoder(jsonObject)
	_ = decoder.Decode(&config)
	_ = jsonObject.Close()
	return config
}

func deferInit() {
	// Use a flagSet to avoid issues during TestMain* tests
	configflag := flag.NewFlagSet("main", flag.ContinueOnError)
	configflag.StringVar(&configPath, "config", "config.json", "/path/to/config.json. (Default: ./config.json)")
	configflag.BoolVar(&prefetch, "prefetch", false, "Prefetch and convert image to webp")
	configflag.IntVar(&jobs, "jobs", runtime.NumCPU(), "Prefetch thread, default is all.")
	configflag.BoolVar(&lazyMode, "lazy", false, "Convert images in the background, asynchronously")
	configflag.IntVar(&maxDefaultJobs, "lazy-jobs", runtime.NumCPU(), "Max parallel tasks (WebP) in lazy mode, default is all.")
	configflag.IntVar(&maxHeavyJobs, "lazy-heavy-jobs", runtime.NumCPU(), "Max parallel heavy tasks (AVIF) in lazy mode, default is all.")
	configflag.BoolVar(&dumpConfig, "dump-config", false, "Print sample config.json")
	configflag.BoolVar(&dumpSystemd, "dump-systemd", false, "Print sample systemd service file.")
	configflag.BoolVar(&verboseMode, "v", false, "Verbose, print out debug info.")
	configflag.BoolVar(&showVersion, "V", false, "Show version information.")
	err := configflag.Parse(os.Args[1:])
	if err != nil {
		log.Error(err)
	}

	// Logrus
	log.SetOutput(os.Stdout)
	log.SetReportCaller(true)
	Formatter := &log.TextFormatter{
		EnvironmentOverrideColors: true,
		FullTimestamp:             true,
		TimestampFormat:           "2006-01-02 15:04:05",
		CallerPrettyfier: func(f *runtime.Frame) (string, string) {
			return fmt.Sprintf("[%d:%s()]", f.Line, f.Function), ""
		},
	}
	log.SetFormatter(Formatter)

	if verboseMode {
		log.SetLevel(log.DebugLevel)
		log.Debug("Debug mode is enabled!")
	} else {
		log.SetLevel(log.InfoLevel)
	}
}

func switchProxyMode() {
	// Check for remote address
	matched, _ := regexp.MatchString(`^https?://`, config.ImgPath)
	proxyMode = false
	if matched {
		proxyMode = true
	} else {
		_, err := os.Stat(config.ImgPath)
		if err != nil {
			log.Fatalf("Your image path %s is incorrect.Please check and confirm.", config.ImgPath)
		}
	}
}

func main() {
	// Our banner
	banner := fmt.Sprintf(`
▌ ▌   ▌  ▛▀▖ ▞▀▖                ▞▀▖
▌▖▌▞▀▖▛▀▖▙▄▘ ▚▄ ▞▀▖▙▀▖▌ ▌▞▀▖▙▀▖ ▌▄▖▞▀▖
▙▚▌▛▀ ▌ ▌▌   ▖ ▌▛▀ ▌  ▐▐ ▛▀ ▌   ▌ ▌▌ ▌
▘ ▘▝▀▘▀▀ ▘   ▝▀ ▝▀▘▘   ▘ ▝▀▘▘   ▝▀ ▝▀

Webp Server Go - v%s
Develop by WebP Server team. https://github.com/webp-sh`, version)

	deferInit()
	// process cli params
	if dumpConfig {
		fmt.Println(sampleConfig)
		os.Exit(0)
	}
	if dumpSystemd {
		fmt.Println(sampleSystemd)
		os.Exit(0)
	}
	if showVersion {
		fmt.Printf("\n %c[1;32m%s%c[0m\n\n", 0x1B, banner+"", 0x1B)
		os.Exit(0)
	}

	config = loadConfig(configPath)
	switchProxyMode()

	VipsConfig = &vips.Config{
		ConcurrencyLevel: runtime.NumCPU(),
		MaxCacheFiles:    1,
	}

	vips.Startup(VipsConfig)
	defer vips.Shutdown()

	if lazyMode {
		log.Info("Lazy mode enabled!")
		DefaultWorkQueue = pq.NewWith(byPriority) // Default tasks queue
		HeavyWorkQueue = pq.NewWith(byPriority)   // Heavy tasks queue
		WorkOngoingSet = hs.New()                 // In-flight operations

		// Create a buffered (non-blocking) pool that can scale up to runtime.NumCPU() workers
		// and has a buffer capacity of 1000 tasks
		DefaultWorkerPool = pond.New(runtime.NumCPU(), 1000)
		defer DefaultWorkerPool.StopAndWait()

		// Heavy tasks are the most resource intensive ones (e.g. Avif)
		HeavyWorkerPool = pond.New(maxHeavyJobs, 1000)
		defer HeavyWorkerPool.StopAndWait()

		Beat = gocron.NewScheduler(time.UTC)
		Beat.SetMaxConcurrentJobs(1, gocron.RescheduleMode)
		_, _ = Beat.Every(lazyTickerPeriod).Seconds().Do(func() {
			lazyDo()
		})
		Beat.StartAsync()
		defer Beat.Stop()
	}

	WriteLock = cache.New(5*time.Minute, 10*time.Minute)

	if prefetch {
		go prefetchImages(config.ImgPath, config.ExhaustPath)
	}

	app := fiber.New(fiber.Config{
		ServerHeader:          "Webp Server Go",
		DisableStartupMessage: true,
	})
	app.Use(etag.New(etag.Config{
		Weak: true,
	}))
	app.Use(logger.New())

	listenAddress := config.Host + ":" + config.Port
	app.Get("/*", convert)

	fmt.Printf("\n %c[1;32m%s%c[0m\n\n", 0x1B, banner, 0x1B)
	fmt.Println("Webp Server Go is Running on http://" + listenAddress)

	_ = app.Listen(listenAddress)
}

// Create jobs from the pools 
func lazyDo() {
	for i := 0; i < DefaultWorkQueue.Size(); i++ {
		DefaultWorkerPool.Submit(convertDefaultWork)
	}
	for i := 0; i < HeavyWorkQueue.Size(); i++ {
		HeavyWorkerPool.Submit(convertHeavyWork)
	}
}
