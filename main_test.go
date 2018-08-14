package main

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestHTTPClient(t *testing.T) {
	Convey("HTTP client sets a sensible timeout", t, func() {
		So(httpClient.Timeout, ShouldEqual, time.Second*10)
	})

	Convey("HTTP client doesn't follow redirects", t, func() {
		req, _ := http.NewRequest("GET", "/", nil)
		err := httpClient.CheckRedirect(req, nil)
		So(err, ShouldEqual, http.ErrUseLastResponse)
	})

	Convey("HTTP client disables connection pooling", t, func() {
		So(httpClient.Transport.(*http.Transport).MaxIdleConns, ShouldEqual, 0)
	})

	Convey("HTTP client allows invalid TLS certificates", t, func() {
		So(httpClient.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify, ShouldBeTrue)
	})
}

func TestBlockAndSend(t *testing.T) {
	Convey("blockAndSend sends to countChan and requestChan for a valid log entry", t, func() {
		countChan := make(chan int, 1)
		requestChan := make(chan request, 1)
		var err error

		go func() {
			replayDate := time.Now().Add(timeOffset)
			replayDateStr := replayDate.Format(timeLayout)
			err = blockAndSend(countChan, requestChan, replayDateStr+` lb-name 1.1.1.1:1 2.2.2.2:2 0.01 0.02 0.03 100 100 0 0 "GET https://some-url:443/some/path HTTP/1.1" "Some Browser" TLS-ALGO TLSv1.2`)
		}()

		timer := time.NewTimer(time.Second * 10)

		var count int
		var req *request
		var stop int

		for {
			if stop >= 2 {
				break
			}
			select {
			case count = <-countChan:
				stop++
			case r := <-requestChan:
				req = &r
				stop++
			case <-timer.C:
				stop = 2
			}
		}

		So(err, ShouldBeNil)
		So(count, ShouldEqual, 1)
		So(req, ShouldNotBeNil)
	})

	Convey("blockAndSend waits for a future log entry", t, func() {
		countChan := make(chan int, 1)
		requestChan := make(chan request, 1)
		var err error

		start := time.Now()

		go func() {
			replayDate := time.Now().Add(timeOffset).Add(time.Second * 3)
			replayDateStr := replayDate.Format(timeLayout)
			err = blockAndSend(countChan, requestChan, replayDateStr+` lb-name 1.1.1.1:1 2.2.2.2:2 0.01 0.02 0.03 100 100 0 0 "GET https://some-url:443/some/path HTTP/1.1" "Some Browser" TLS-ALGO TLSv1.2`)
		}()

		timer := time.NewTimer(time.Second * 10)

		var count int
		var req *request
		var stop int

		for {
			if stop >= 2 {
				break
			}
			select {
			case count = <-countChan:
				stop++
			case r := <-requestChan:
				req = &r
				stop++
			case <-timer.C:
				stop = 2
			}
		}

		end := time.Now()

		So(err, ShouldBeNil)
		So(count, ShouldEqual, 1)
		So(req, ShouldNotBeNil)
		So(end.Sub(start), ShouldBeGreaterThan, time.Second*3)
	})

	Convey("blockAndSend returns an error for an invalid log entry", t, func() {
		countChan := make(chan int, 1)
		requestChan := make(chan request, 1)
		var err error
		var logEntry string

		go func() {
			replayDate := time.Now().Add(timeOffset)
			replayDateStr := replayDate.Format(timeLayout)
			logEntry = replayDateStr + ` 1.1.1.1:1 2.2.2.2:2 0.01 0.02 0.03 100 100 0 0 "GET https://some-url:443/some/path HTTP/1.1" "Some Browser" TLS-ALGO TLSv1.2`
			err = blockAndSend(countChan, requestChan, logEntry)
		}()

		timer := time.NewTimer(time.Second * 10)

		var count int
		var req *request
		var stop int

		for {
			if stop >= 2 || err != nil {
				break
			}
			select {
			case count = <-countChan:
				stop++
			case r := <-requestChan:
				req = &r
				stop++
			case <-timer.C:
				stop = 2
			default:
			}
		}

		So(err.Error(), ShouldEqual, "error reading log entry: failed to parse log entry: `"+logEntry+"`")
		So(count, ShouldEqual, 0)
		So(req, ShouldBeNil)
	})

	Convey("blockAndSend returns an error for an old log entry", t, func() {
		countChan := make(chan int, 1)
		requestChan := make(chan request, 1)
		var err error
		var logEntry string

		go func() {
			replayDate := time.Now().Add(timeOffset).Add(-time.Second * 5)
			replayDateStr := replayDate.Format(timeLayout)
			logEntry = replayDateStr + ` lb-name 1.1.1.1:1 2.2.2.2:2 0.01 0.02 0.03 100 100 0 0 "GET https://some-url:443/some/path HTTP/1.1" "Some Browser" TLS-ALGO TLSv1.2`
			err = blockAndSend(countChan, requestChan, logEntry)
		}()

		timer := time.NewTimer(time.Second * 10)

		var count int
		var req *request
		var stop int

		for {
			if stop >= 2 || err != nil {
				break
			}
			select {
			case count = <-countChan:
				stop++
			case r := <-requestChan:
				req = &r
				stop++
			case <-timer.C:
				stop = 2
			default:
			}
		}

		So(err, ShouldEqual, errRequestTooLate)
		So(count, ShouldEqual, 0)
		So(req, ShouldBeNil)
	})
}

func TestGetTimestampAndURL(t *testing.T) {
	var tests = []struct {
		line string
		t    string
		u    string
		e    error
	}{
		{
			`2018-08-03T00:00:07.627865Z lb-name 1.1.1.1:1 2.2.2.2:2 0.01 0.02 0.03 100 100 0 0 "GET https://some-url:443/some/path HTTP/1.1" "Some Browser" TLS-ALGO TLSv1.2`,
			"2018-08-03 00:00:07.627865 +0000 UTC",
			"https://some-url:443/some/path",
			nil,
		},
		{
			`15-01-2010T00:00:07.627865Z lb-name 1.1.1.1:1 2.2.2.2:2 0.01 0.02 0.03 100 100 0 0 "GET https://some-url:443/some/path HTTP/1.1" "Some Browser" TLS-ALGO TLSv1.2`,
			"0001-01-01 00:00:00 +0000 UTC",
			"",
			errors.New(`parsing time "15-01-2010T00:00:07.627865Z" as "2006-01-02T15:04:05.999999999Z": cannot parse "1-2010T00:00:07.627865Z" as "2006"`),
		},
		{
			`2018-08-03T00:00:07.627865Z lb-name 1.1.1.1:1 2.2.2.2:2 0.01 0.02 0.03 100 100 0 0 "GET :not-valid HTTP/1.1" "Some Browser" TLS-ALGO TLSv1.2`,
			"0001-01-01 00:00:00 +0000 UTC",
			"",
			errors.New("parse :not-valid: missing protocol scheme"),
		},
		{
			`not enough columns`,
			"0001-01-01 00:00:00 +0000 UTC",
			"",
			errors.New("failed to parse log entry: `not enough columns`"),
		},
	}
	Convey("getTimestampAndURL returns valid time and URL, or error on failure", t, func() {
		for _, test := range tests {
			t, u, e := getTimestampAndURL(test.line)
			So(t.String(), ShouldEqual, test.t)
			if len(test.u) == 0 {
				So(u, ShouldBeNil)
			} else {
				So(u.String(), ShouldEqual, test.u)
			}
			if test.e == nil {
				So(e, ShouldBeNil)
			} else {
				So(e.Error(), ShouldEqual, test.e.Error())
			}
		}
	})
}

type testTransport struct {
	req         *http.Request
	resp        *http.Response
	shouldError bool
}

type testReader struct {
	io.ReadCloser
	isRead      bool
	shouldError bool
	shouldPanic bool
}

func (tr *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.req = req
	if tr.shouldError {
		return nil, errors.New("transport error")
	}
	tr.resp = &http.Response{
		Body: &testReader{},
	}
	return tr.resp, nil
}

func (tr *testReader) Read([]byte) (int, error) {
	if tr.shouldPanic {
		panic("read panic")
	}
	tr.isRead = true
	if tr.shouldError {
		return 0, errors.New("read error")
	}
	return 0, io.EOF
}

func TestSendRequest(t *testing.T) {
	oldCli := httpClient
	defer func() {
		httpClient = oldCli
	}()

	transport := &testTransport{}
	httpClient = &http.Client{
		Transport: transport,
	}

	u, _ := url.Parse("http://test-host/test/path")

	Convey("sendRequest should pass a request to the httpClient", t, func() {
		err := sendRequest(u)
		So(err, ShouldBeNil)
		So(transport.req.Host, ShouldEqual, "test-host")
	})

	Convey("sendRequest should use testHostFlag if it's set", t, func() {
		testHostFlag = "another-host"
		err := sendRequest(u)
		testHostFlag = ""
		So(err, ShouldBeNil)
		So(transport.req.Host, ShouldEqual, "another-host")
	})

	Convey("sendRequest returns HTTP client errors", t, func() {
		transport.shouldError = true
		err := sendRequest(u)
		transport.shouldError = false
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldEqual, "Error sending request for http://another-host/test/path: Get http://another-host/test/path: transport error")
	})

	Convey("sendRequest reads response body", t, func() {
		err := sendRequest(u)
		So(err, ShouldBeNil)
		So(transport.resp.Body.(*testReader).isRead, ShouldBeTrue)
	})

	Convey("sendRequest ignores read errors", t, func() {
		transport.resp.Body.(*testReader).shouldError = true
		err := sendRequest(u)
		transport.resp.Body.(*testReader).shouldError = false
		So(err, ShouldBeNil)
		So(transport.resp.Body.(*testReader).isRead, ShouldBeTrue)
	})

	Convey("sendRequest ignores panics", t, func() {
		transport.resp.Body.(*testReader).shouldPanic = true
		var err error
		So(func() {
			err = sendRequest(u)
		}, ShouldNotPanic)
		transport.resp.Body.(*testReader).shouldPanic = false
		So(err, ShouldBeNil)
	})
}
