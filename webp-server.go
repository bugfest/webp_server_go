package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"runtime"

	"github.com/davidbyttow/govips/v2/vips"
	badger "github.com/dgraph-io/badger/v3"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/etag"
	"github.com/gofiber/fiber/v2/middleware/logger"
	log "github.com/sirupsen/logrus"
)

var db *badger.DB

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
	fs:=flag.NewFlagSet("webp-server", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "config.json", "/path/to/config.json. (Default: ./config.json)")
	fs.BoolVar(&prefetch, "prefetch", false, "Prefetch and convert image to webp")
	fs.IntVar(&jobs, "jobs", runtime.NumCPU(), "Prefetch thread, default is all.")
	fs.BoolVar(&dumpConfig, "dump-config", false, "Print sample config.json")
	fs.BoolVar(&dumpSystemd, "dump-systemd", false, "Print sample systemd service file.")
	fs.BoolVar(&verboseMode, "v", false, "Verbose, print out debug info.")
	fs.BoolVar(&showVersion, "V", false, "Show version information.")
	err := fs.Parse(os.Args[1:])
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

func dbSetup() (*badger.DB, error) {
	opts := badger.DefaultOptions(config.DBPath)

	// https://github.com/dgraph-io/badger/issues/1297#issuecomment-612941482
	opts.NumVersionsToKeep = 0
	opts.CompactL0OnClose = true                                                                                                                                                                                                                                                                                                              
	opts.NumLevelZeroTables = 1
	opts.NumLevelZeroTablesStall = 2
	opts.ValueLogFileSize = 1024 * 1024 * 10
	return badger.Open(opts)
}

func main() {
	// Our banner
	banner := fmt.Sprintf(`
▌ ▌   ▌  ▛▀▖ ▞▀▖                ▞▀▖
▌▖▌▞▀▖▛▀▖▙▄▘ ▚▄ ▞▀▖▙▀▖▌ ▌▞▀▖▙▀▖ ▌▄▖▞▀▖
▙▚▌▛▀ ▌ ▌▌   ▖ ▌▛▀ ▌  ▐▐ ▛▀ ▌   ▌ ▌▌ ▌
▘ ▘▝▀▘▀▀ ▘   ▝▀ ▝▀▘▘   ▘ ▝▀▘▘   ▝▀ ▝▀

Webp Server Go - v%s
Developed by WebP Server team. https://github.com/webp-sh`, version)

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

	vips.Startup(&vips.Config{
		ConcurrencyLevel: runtime.NumCPU(),
	})
	defer vips.Shutdown()

	db, err := dbSetup()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

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
