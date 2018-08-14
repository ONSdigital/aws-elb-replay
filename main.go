package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Create a HTTP client with sensible defaults
var httpClient = &http.Client{
	// Disable redirects, some requests have endless redirect loops
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
	// Set a timeout
	Timeout: time.Second * 10,
	// Disable connection pooling and allow insecure TLS
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	},
}

// errRequestTooLate is sent when the request is over 5 seconds old
var errRequestTooLate = errors.New("request too late")

// Flag variables
var testHostFlag = ""
var offsetFlag = 14
var concurrencyFlag = 50

// Useful defaults
var timeOffset = -1 * time.Hour * 24

// Time layout used by ELB logs
const timeLayout = "2006-01-02T15:04:05.999999999Z"

// Request struct used to pass replay requests to the request send loop
type request struct {
	u *url.URL
	d time.Time
}

// Request counters
var sentRequests, expectedRequests, lastSent, lastExpected int

// Wait groups
var requestWg, filesWg sync.WaitGroup

// Channels
var requestChan = make(chan request, 20)
var countChan = make(chan int)

// Regex used for parsing ELB log file entries
// From https://aws.amazon.com/blogs/aws/access-logs-for-elastic-load-balancers/
var lineRe = regexp.MustCompile("([^ ]*) ([^ ]*) ([^ ]*):([0-9]*) ([^ ]*):([0-9]*) ([.0-9]*) ([.0-9]*) ([.0-9]*) (-|[0-9]*) (-|[0-9]*) ([-0-9]*) ([-0-9]*) \"([^ ]*) ([^ ]*) (- |[^ ]*)\".*")

func init() {
	flag.IntVar(&offsetFlag, "offset", 14, "the number of days to offset by")
	flag.IntVar(&concurrencyFlag, "concurrency", 50, "the number of concurrent requests to send")
	flag.StringVar(&testHostFlag, "host", "", "the test host to replay traffic against")
	flag.Parse()

	timeOffset *= time.Duration(offsetFlag)
}

func main() {
	// Get requests from channel
	defer requestWg.Wait()
	requestLoop()

	// Replay log files
	logFileLoop()
}

func requestLoop() {
	// Use a semaphore to limit in-flight requests
	sem := make(chan struct{}, concurrencyFlag)

	// Print a status update once a second
	timer := time.NewTicker(time.Second * 1)

	requestWg.Add(1)
	go func() {
		defer requestWg.Done()
		for {
			select {
			case <-timer.C:
				sentSec, expectedSec := sentRequests-lastSent, expectedRequests-lastExpected
				log.Printf("%d total sent, %d total expected, %d delayed (%d sent, %d expected)\n", sentRequests, expectedRequests, expectedRequests-sentRequests, sentSec, expectedSec)
				lastSent, lastExpected = sentRequests, expectedRequests
			case <-countChan:
				expectedRequests++
			case req := <-requestChan:
				sem <- struct{}{}
				requestWg.Add(1)
				sentRequests++
				go func() {
					defer func() {
						requestWg.Done()
						<-sem
					}()
					err := sendRequest(req.u)
					if err != nil {
						log.Printf("error in sendRequest: %s", err)
					}
				}()
			}
		}
	}()
}

// Looks for log files matching the current offset and replays them
func logFileLoop() {
	for {
		startDate := time.Now().Add(timeOffset)
		logFiles := findLogFiles(startDate)

		if len(logFiles) == 0 {
			// If we don't find any, we assume we're done and allow the
			// replayer to exit once in-flight requests have finished
			break
		}

		for _, f := range logFiles {
			path := f
			filesWg.Add(1)
			go func() {
				defer filesWg.Done()

				f, err := os.Open(path)
				if err != nil {
					panic(err)
				}

				defer f.Close()
				replayLogFile(f)
			}()
		}

		filesWg.Wait()

		startDate = startDate.Add(time.Hour)
	}
}

// Finds log files for the current offset
func findLogFiles(startDate time.Time) []string {
	logPath := fmt.Sprintf("logs/%04d/%02d/%02d/*%04d%02d%02dT%02d00Z*", startDate.Year(), startDate.Month(), startDate.Day(), startDate.Year(), startDate.Month(), startDate.Day(), startDate.Hour())

	matches, err := filepath.Glob(logPath)
	if err != nil {
		panic(err)
	}

	return matches
}

// Replays a log file
func replayLogFile(r io.Reader) {
	rdr := bufio.NewReader(r)

	for {
		b, err := rdr.ReadBytes('\n')
		if err != nil && err != io.EOF {
			log.Printf("error in replayLogFile: %s", err)
			break
		}

		if len(b) > 0 {
			err := blockAndSend(countChan, requestChan, strings.TrimSpace(string(b)))
			if err != nil && err != errRequestTooLate {
				log.Printf("error in blockAndSend: %s", err)
			}
		}

		if err == io.EOF {
			break
		}
	}
}

// Waits until the correct time to send the next request
func blockAndSend(countChan chan int, requestChan chan request, req string) error {
	reqDate, u, err := getTimestampAndURL(req)
	if err != nil {
		return fmt.Errorf("error reading log entry: %s", err)
	}

	// Work out when the request should be sent
	replayDate := reqDate.Add(timeOffset * -1)

	if replayDate.Before(time.Now().Add(time.Second * -5)) {
		// We're more than 5 seconds out, so ignore this request
		// to avoid sending a large number of unwanted requests

		// This handles scenarios where requests are getting backed up,
		// or where the log file has entries earlier than the initial
		// offset used by the replayer
		return errRequestTooLate
	}

	if replayDate.After(time.Now()) {
		// Wait until the right time to send the request
		waitTime := replayDate.Sub(time.Now())
		time.Sleep(waitTime)
	}

	countChan <- 1
	requestChan <- request{u, replayDate}

	return nil
}

// Get the time and URL from the ELB log entry
func getTimestampAndURL(req string) (time.Time, *url.URL, error) {
	matches := lineRe.FindStringSubmatch(req)

	// 17 is the number of "columns" in an ELB log file (see lineRe)
	if len(matches) != 17 {
		return time.Time{}, nil, fmt.Errorf("failed to parse log entry: `%s`", req)
	}

	reqDateStr := matches[1]
	reqPathStr := matches[15]

	reqDate, err := time.Parse(timeLayout, reqDateStr)
	if err != nil {
		log.Printf("Failed to parse timestamp `%s` for `%s`: %s", reqDateStr, req, err)
		return time.Time{}, nil, err
	}

	u, err := url.Parse(reqPathStr)
	if err != nil {
		return time.Time{}, nil, err
	}

	return reqDate, u, nil
}

// Sends a request and consumes the response body
func sendRequest(u *url.URL) error {
	defer func() {
		// Ignore the panic so the replayer continues
		// TODO probably want to handle this properly, or at least log it
		recover()
	}()

	if len(testHostFlag) > 0 {
		// Overwrite the host using the one set on the command line
		// Note: this will affect the Host header, so may not work in all cases
		u.Host = testHostFlag
	}

	res, err := httpClient.Get(u.String())
	if err != nil {
		return fmt.Errorf("Error sending request for %s: %s", u.String(), err)
	}

	defer res.Body.Close()

	// Discard the request body - we don't care about reusing the connection
	// since connection pooling is disabled, but this forces the remote host to
	// actually return all of the bytes we requested.
	io.Copy(ioutil.Discard, res.Body)

	return nil
}
