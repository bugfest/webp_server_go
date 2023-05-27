package main

import (
	"net/http"
	"net/url"

	// "os"
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
	)
	// Sometimes reqURIwithQuery can be https://example.tld/mypic/123.jpg?someother=200&somebugs=200, we need to extract it.
	u, err := url.Parse(reqURIwithQuery)
	if err != nil {
		log.Errorln(err)
	}
	reqURIwithQuery = u.RequestURI()
	// delete ../ in reqURI to mitigate directory traversal
	reqURI = path.Clean(reqURI)
	reqURIwithQuery = path.Clean(reqURIwithQuery)

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
	if proxyMode {
		rawImageAbs = config.ImgPath + reqURIwithQuery // https://test.webp.sh/mypic/123.jpg?someother=200&somebugs=200
	} else {
		rawImageAbs = path.Join(config.ImgPath, reqURI) // /home/xxx/mypic/123.jpg
	}
	log.Debugf("Incoming connection from %s %s", c.IP(), imgFilename)

	if !checkAllowedType(imgFilename) {
		msg := "File extension not allowed! " + imgFilename
		log.Warn(msg)
		c.Status(http.StatusBadRequest)
		_ = c.Send([]byte(msg))
		return nil
	}

	goodFormat := guessSupportedFormat(&c.Request().Header)

	if proxyMode {
		_, rawImageAbs, _ = proxyHandler(c, reqURIwithQuery)
	}

	log.Debugf("rawImageAbs=%s", rawImageAbs)

	// Check the original image for existence,
	if !imageExists(rawImageAbs) {
		msg := "image not found"
		_ = c.Send([]byte(msg))
		log.Warn(msg)
		_ = c.SendStatus(404)
		return nil
	}

	// generate with timestamp to make sure files are update-to-date
	// If extraParams not enabled, exhaust path will be /home/webp_server/exhaust/path/to/tsuki.jpg.1582558990.webp
	// If extraParams enabled, and given request at tsuki.jpg?width=200, exhaust path will be /home/webp_server/exhaust/path/to/tsuki.jpg.1582558990.webp_width=200&height=0
	// If extraParams enabled, and given request at tsuki.jpg, exhaust path will be /home/webp_server/exhaust/path/to/tsuki.jpg.1582558990.webp_width=0&height=0
	avifAbs, webpAbs := genOptimizedAbsPath(rawImageAbs, config.ExhaustPath, imgFilename, reqURI, extraParams)
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

	// etag := genEtag(finalFileName)
	// c.Set("ETag", etag)
	c.Set("X-Compression-Rate", getCompressionRate(rawImageAbs, finalFileName))
	return c.SendFile(finalFileName)
}

func proxyHandler(c *fiber.Ctx, reqURIwithQuery string) (bool, string, error) {

	// https://test.webp.sh/mypic/123.jpg?someother=200&somebugs=200
	realRemoteAddr := config.ImgPath + reqURIwithQuery

	// Since we cannot store file in format of "mypic/123.jpg?someother=200&somebugs=200", we need to hash it.
	reqURIwithQueryHash := Sha1Path(reqURIwithQuery) // 378e740ca56144b7587f3af9debeee544842879a
	localRawImagePath := path.Join(remoteRaw, reqURIwithQueryHash) // To store the remote raw image, /home/webp_server/remote-raw/378e740ca56144b7587f3af9debeee544842879a
	localRawMetaPath := path.Join(remoteRaw, reqURIwithQueryHash + ".meta") // To store the remote raw metadata, /home/webp_server/remote-raw/378e740ca56144b7587f3af9debeee544842879a.meta

	// cleanProxyCache(config.ExhaustPath + reqURIwithQuery + "*")
	refresh, err := fetchRemoteImage(localRawImagePath, realRemoteAddr, localRawMetaPath)
	if err != nil {
		_ = c.SendStatus(500)
		return false, "", err
	}
	return refresh, localRawImagePath, err
}
