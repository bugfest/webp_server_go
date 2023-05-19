package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"path"
	"strconv"

	"github.com/gofiber/fiber/v2"
	log "github.com/sirupsen/logrus"
)

func convert(c *fiber.Ctx) error {
	//basic vars
	var (
		reqURI, _          = url.QueryUnescape(c.Path())        // /mypic/123.jpg
		reqURIwithQuery, _ = url.QueryUnescape(c.OriginalURL()) // /mypic/123.jpg?someother=200&somebugs=200
		imgFilename        = path.Base(reqURI)                  // pure filename, 123.jpg
		reqHostname        = c.Hostname()
	)

	if proxyMode {
		// Don't deal with the encoding to avoid upstream compatibilities
		reqURI = c.Path()
		reqURIwithQuery = c.OriginalURL()
	}

	// Sometimes reqURIwithQuery can be https://example.tld/mypic/123.jpg?someother=200&somebugs=200, we need to extract it.
	u, err := url.Parse(reqURIwithQuery)
	if err != nil {
		log.Errorln(err)
	}

	reqURIwithQuery = u.RequestURI()

	if !proxyMode {
		// delete ../ in reqURI to mitigate directory traversal in
		reqURI = path.Clean(reqURI)
		reqURIwithQuery = path.Clean(reqURIwithQuery)
	}

	// Begin Extra params
	var extraParams ExtraParams
	Width := c.Query("width")
	Height := c.Query("height")
	WidthInt, err := strconv.Atoi(Width)
	if err != nil {
		WidthInt = 0
	}
	HeightInt, err := strconv.Atoi(Height)
	if err != nil {
		HeightInt = 0
	}
	extraParams = ExtraParams{
		Width:  WidthInt,
		Height: HeightInt,
	}
	// End Extra params

	var rawImageAbs string
	rawImageAbs = path.Join(config.ImgPath, reqURI) // /home/xxx/mypic/123.jpg

	log.Debugf("Incoming connection from %s %s %s", c.IP(), reqHostname, imgFilename)

	if !checkAllowedType(imgFilename) {
		msg := "File extension not allowed! " + imgFilename
		log.Warn(msg)
		c.Status(http.StatusBadRequest)
		_ = c.Send([]byte(msg))
		return nil
	}

	goodFormat := guessSupportedFormat(&c.Request().Header)

	if proxyMode {
		rawImageAbs, _ = proxyHandler(c, reqURIwithQuery, reqHostname)
	}

	// Raw image path will be remote-raw/<hostname>/<hash(reqURIwithQuery)>
	log.Debugf("rawImageAbs=%s", rawImageAbs)

	// Check the original image for existence,
	if !imageExists(rawImageAbs) {
		msg := "image not found"
		log.Infof(msg+": %s", rawImageAbs)
		_ = c.Send([]byte(msg))
		_ = c.SendStatus(404)
		return nil
	}

	// Normal mode:
	// - generate with timestamp to make sure files are update-to-date
	// - If extraParams not enabled, exhaust path will be /home/webp_server/exhaust/path/to/tsuki.jpg.1582558990.webp
	// - If extraParams enabled, and given request at tsuki.jpg?width=200, exhaust path will be /home/webp_server/exhaust/path/to/tsuki.jpg.1582558990.webp_width=200&height=0
	// - If extraParams enabled, and given request at tsuki.jpg, exhaust path will be /home/webp_server/exhaust/path/to/tsuki.jpg.1582558990.webp_width=0&height=0
	// Proxy mode:
	// - generate with timestamp to make sure files are update-to-date
	// - Exhaust path will be /home/webp_server/exhaust/<hostname>/<hash(reqURIwithQuery)>.1582558990.webp
	targetImageFilename := path.Join(reqHostname, path.Base(rawImageAbs))
	log.Debugf("targetImageFilename=%s", targetImageFilename)

	avifAbs, webpAbs := genOptimizedAbsPath(rawImageAbs, config.ExhaustPath, targetImageFilename, extraParams)
	convertFilter(rawImageAbs, avifAbs, webpAbs, extraParams, nil)

	var availableFiles = []string{rawImageAbs}
	for _, v := range goodFormat {
		if v == "avif" {
			availableFiles = append(availableFiles, avifAbs)
		}
		if v == "webp" {
			availableFiles = append(availableFiles, webpAbs)
		}
	}

	var finalFileName = findSmallestFiles(availableFiles)
	var finalFileExtension = path.Ext(finalFileName)
	if finalFileExtension == ".webp" {
		c.Set("Content-Type", "image/webp")
	} else if finalFileExtension == ".avif" {
		c.Set("Content-Type", "image/avif")
	}

	c.Set("X-Compression-Rate", getCompressionRate(rawImageAbs, finalFileName))
	return c.SendFile(finalFileName)
}

func proxyHandler(c *fiber.Ctx, reqURIwithQuery string, reqHostname string) (string, error) {

	BackendURL := config.Proxy.BackendURL

	// https://test.webp.sh/mypic/123.jpg?someother=200&somebugs=200
	realRemoteAddr := BackendURL + reqURIwithQuery

	// Rewrite the target backend if a mapping rule matches the hostname
	if v, found := config.Proxy.HostMap[reqHostname]; found {
		log.Debugf("Found mapping %s to %s", reqHostname, v.BackendURL)
		realRemoteAddr = v.BackendURL + reqURIwithQuery
	}

	// Ping Remote for status code and etag info
	log.Infof("Remote Addr is %s, fetching info...", realRemoteAddr)
	statusCode, etagValue, _ := getRemoteImageInfo(realRemoteAddr)

	// Since we cannot store file in format of "/mypic/123.jpg?someother=200&somebugs=200", we need to hash it.
	reqURIwithQueryHash := Sha1Path(reqURIwithQuery) // 378e740ca56144b7587f3af9debeee544842879a
	etagValueHash := Sha1Path(etagValue)             // 123e740ca56333b7587f3af9debeee5448428123

	localRawImagePath := path.Join(remoteRaw, reqURIwithQueryHash+"-etag-"+etagValueHash) // For store the remote raw image, /home/webp_server/remote-raw/378e740ca56144b7587f3af9debeee544842879a-etag-123e740ca56333b7587f3af9debeee5448428123

	if statusCode == 200 {
		if imageExists(localRawImagePath) {
			return localRawImagePath, nil
		} else {
			// Temporary store of remote file.
			cleanProxyCache(config.ExhaustPath + reqURIwithQuery + "*")
			log.Info("Remote file not found in remote-raw path, fetching...")
			// cleanProxyCache(config.ExhaustPath + reqURIwithQuery) // This is never going to clean anything as we're using hashes now
			err := fetchRemoteImage(localRawImagePath, realRemoteAddr)
			return localRawImagePath, err
		}
	} else {
		msg := fmt.Sprintf("Remote returned %d status code!", statusCode)
		_ = c.Send([]byte(msg))
		log.Warn(msg)
		_ = c.SendStatus(statusCode)
		cleanProxyCache(config.ExhaustPath + reqURIwithQuery + "*")
		return "", errors.New(msg)
	}
}
