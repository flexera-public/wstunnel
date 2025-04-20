// Copyright (c) 2015 RightScale, Inc. - see LICENSE

package tunnel

// Omega: Alt+937

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"gopkg.in/inconshreveable/log15.v2"
)

// Our simple proxy server. This server: only handles proxying of HTTPS data via
// CONNECT protocol, not HTTP. Also we don't bother to modify headers, such as
// adding X-Forwarded-For as we don't test that.
var proxyErrorLog string
var proxyConnCount int
var proxyServer *httptest.Server

func copyAndClose(w, r net.Conn) {
	connOk := true
	if _, err := io.Copy(w, r); err != nil {
		connOk = false
	}
	if err := r.Close(); err != nil && connOk {
		proxyErrorLog += fmt.Sprintf("Error closing: %s\n", err)
	}
}

func externalProxyServer(w http.ResponseWriter, r *http.Request) {
	//log.Printf("Proxy server got %#v\n", r)
	proxyConnCount++
	log15.Info("externalProxyServer proxying", "url", r.RequestURI)

	if r.Method != "CONNECT" {
		errMsg := "CONNECT not passed to proxy"
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte(errMsg)); err != nil {
			proxyErrorLog += fmt.Sprintf("Error writing response: %s\n", err)
		}
		proxyErrorLog += errMsg
		return
	}
	hij, ok := w.(http.Hijacker)
	if !ok {
		errMsg := "Typecast to hijack failed!"
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte(errMsg)); err != nil {
			proxyErrorLog += fmt.Sprintf("Error writing response: %s\n", err)
		}
		proxyErrorLog += errMsg
		return
	}

	host := r.URL.Host
	targetSite, err := net.Dial("tcp", host)
	if err != nil {
		errMsg := "Cannot establish connection to upstream server!"
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte(errMsg)); err != nil {
			proxyErrorLog += fmt.Sprintf("Error writing response: %s\n", err)
		}
		proxyErrorLog += errMsg
		return
	}

	proxyClient, _, err := hij.Hijack()
	if err != nil {
		errMsg := "Cannot Hijack connection!"
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte(errMsg)); err != nil {
			proxyErrorLog += fmt.Sprintf("Error writing response: %s\n", err)
		}
		proxyErrorLog += errMsg
		return
	}

	res := fmt.Sprintf("%s 200 OK\r\n\r\n", r.Proto)
	if _, err := proxyClient.Write([]byte(res)); err != nil {
		proxyErrorLog += fmt.Sprintf("Error writing response: %s\n", err)
		return
	}

	// Transparent pass through from now on
	go copyAndClose(targetSite, proxyClient)
	go copyAndClose(proxyClient, targetSite)
}

var startClient = func(wstunToken string, wstunHost string, proxy *url.URL, server *ghttp.Server) *WSTunnelClient {
	tunnel, _ := url.Parse("ws://" + wstunHost)
	wstuncli := &WSTunnelClient{
		Token:          wstunToken,
		Tunnel:         tunnel,
		Timeout:        30 * time.Second,
		Proxy:          proxy,
		Log:            log15.Root().New("pkg", "WStuncli"),
		InternalServer: server,
	}
	if err := wstuncli.Start(); err != nil {
		log15.Error("Error starting client", "error", err)
		os.Exit(1)
	}
	log15.Info("Client started")
	return wstuncli
}

var _ = Describe("Testing requests", func() {

	var server *ghttp.Server
	var wstunsrv *WSTunnelServer
	var wstuncli *WSTunnelClient
	var wstunURL string
	var wstunToken string
	var wstunHost string
	var proxyURL *url.URL

	BeforeEach(func() {
		wstunToken = "test567890123456-" + strconv.Itoa(rand.Int()%1000000)
	})

	var waitConnected = func(cli *WSTunnelClient) {
		for !cli.Connected {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// we runs tests twice: once against an internal server and once against an
	// external server, this function runs the tests and we call it twice with a
	// different set-up
	var runTests = func() {
		// Perform the test by running main() with the command line args set
		It("Responds to hello requests", func() {
			wstuncli = startClient(wstunToken, wstunHost, proxyURL, server)
			waitConnected(wstuncli)

			server.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/hello"),
					ghttp.RespondWith(200, `WORLD`, http.Header{"Content-Type": []string{"text/world"}}),
				),
			)

			resp, err := http.Get(wstunURL + "/_token/" + wstunToken + "/hello")
			Ω(err).ShouldNot(HaveOccurred())
			respBody, err := io.ReadAll(resp.Body)
			Ω(err).ShouldNot(HaveOccurred())
			Ω(string(respBody)).Should(Equal("WORLD"))
			Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
			Ω(resp.StatusCode).Should(Equal(200))
		})

		It("Handles a very large request", func() {
			// init and fill a 12MB buffer
			reqSize := 12 * 1024 * 1024 // 12MB
			reqSizeStr := strconv.Itoa(reqSize)
			reqBody := make([]byte, reqSize)
			for i := range reqBody {
				reqBody[i] = byte(i % 256)
			}

			wstuncli = startClient(wstunToken, wstunHost, proxyURL, server)
			waitConnected(wstuncli)

			server.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/large-request"),
					ghttp.VerifyHeaderKV("Content-Length", reqSizeStr),
					ghttp.RespondWith(200, `WORLD`,
						http.Header{"Content-Type": []string{"text/world"}}),
				),
			)

			resp, err := http.Post(wstunURL+"/_token/"+wstunToken+"/large-request",
				"text/binary", bytes.NewReader(reqBody))
			Ω(err).ShouldNot(HaveOccurred())
			respBody, err := io.ReadAll(resp.Body)
			Ω(err).ShouldNot(HaveOccurred())
			Ω(string(respBody)).Should(Equal("WORLD"))
			Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
			Ω(resp.StatusCode).Should(Equal(200))
		})

		It("Handles a very large response", func() {
			// init and fill a 12MB buffer
			respSize := 12 * 1024 * 1024 // 12MB
			respBody := make([]byte, respSize)
			for i := range respBody {
				respBody[i] = byte(i % 256)
			}

			wstuncli = startClient(wstunToken, wstunHost, proxyURL, server)
			waitConnected(wstuncli)

			server.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/large-response"),
					ghttp.RespondWith(200, respBody,
						http.Header{"Content-Type": []string{"text/binary"}}),
				),
			)

			resp, err := http.Get(wstunURL + "/_token/" + wstunToken + "/large-response")
			Ω(err).ShouldNot(HaveOccurred())
			respRecv, err := io.ReadAll(resp.Body)
			Ω(err).ShouldNot(HaveOccurred())
			Ω(respRecv).Should(Equal(respBody))
			Ω(resp.Header.Get("Content-Type")).Should(Equal("text/binary"))
			Ω(resp.StatusCode).Should(Equal(200))
		})

		It("Gets error status", func() {
			wstuncli = startClient(wstunToken, wstunHost, proxyURL, server)
			waitConnected(wstuncli)

			server.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/hello"),
					ghttp.RespondWith(445, `WORLD`, http.Header{"Content-Type": []string{"text/world"}}),
				),
			)

			resp, err := http.Get(wstunURL + "/_token/" + wstunToken + "/hello")
			Ω(err).ShouldNot(HaveOccurred())
			respBody, err := io.ReadAll(resp.Body)
			Ω(err).ShouldNot(HaveOccurred())
			Ω(string(respBody)).Should(Equal("WORLD"))
			Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
			Ω(resp.StatusCode).Should(Equal(445))
		})

		It("Does 100 requests", func() {
			wstuncli = startClient(wstunToken, wstunHost, proxyURL, server)
			waitConnected(wstuncli)

			const N = 100
			for i := 0; i < N; i++ {
				txt := fmt.Sprintf("/hello/%d", i)
				server.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", txt),
						ghttp.RespondWith(200, txt,
							http.Header{"Content-Type": []string{"text/world"}}),
					),
				)
			}

			for i := 0; i < N; i++ {
				txt := fmt.Sprintf("/hello/%d", i)
				resp, err := http.Get(wstunURL + "/_token/" + wstunToken + txt)
				Ω(err).ShouldNot(HaveOccurred())
				respBody, err := io.ReadAll(resp.Body)
				Ω(err).ShouldNot(HaveOccurred())
				Ω(string(respBody)).Should(Equal(txt))
				Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
				Ω(resp.StatusCode).Should(Equal(200))
			}
		})

		It("Does many requests with random sleeps", func() {
			wstuncli = startClient(wstunToken, wstunHost, proxyURL, server)
			waitConnected(wstuncli)

			const N = 20
			server.RouteToHandler("GET", regexp.MustCompile(`^/hello/`),
				func(w http.ResponseWriter, req *http.Request) {
					var i int
					n, err := fmt.Sscanf(req.RequestURI, "/hello/%d", &i)
					if n != 1 || err != nil {
						w.WriteHeader(400)
					} else {
						time.Sleep(time.Duration(10*i) * time.Millisecond)
						w.Header().Set("Content-Type", "text/world")
						w.WriteHeader(200)
						if _, err := fmt.Fprintf(w, "/hello/%d", i); err != nil {
							log15.Error("Error writing response", "error", err)
						}
					}
				})

			resp := make([]*http.Response, N)
			err := make([]error, N)
			wg := sync.WaitGroup{}
			wg.Add(N)
			for i := 0; i < N; i++ {
				go func(i int) {
					txt := fmt.Sprintf("/hello/%d", i)
					resp[i], err[i] = http.Get(wstunURL + "/_token/" + wstunToken + txt)
					wg.Done()
				}(i)
			}
			wg.Wait()
			for i := 0; i < N; i++ {
				txt := fmt.Sprintf("/hello/%d", i)
				Ω(err[i]).ShouldNot(HaveOccurred())
				respBody, err := io.ReadAll(resp[i].Body)
				Ω(err).ShouldNot(HaveOccurred())
				Ω(string(respBody)).Should(Equal(txt))
				Ω(resp[i].Header.Get("Content-Type")).Should(Equal("text/world"))
				Ω(resp[i].StatusCode).Should(Equal(200))
			}
		})
	}

	// wstunnel used as a go library integrated into another application
	Context("Internal requests", func() {
		var saved string
		BeforeEach(func() {
			saved = VV
			VV = "fooey"
			server = ghttp.NewUnstartedServer()

			l, _ := net.Listen("tcp", "127.0.0.1:0")
			wstunHost = l.Addr().String()
			wstunsrv = NewWSTunnelServer([]string{})
			wstunsrv.Start(l)
			wstunURL = "http://" + wstunHost

			log15.Info("Server started")
		})
		AfterEach(func() {
			wstuncli.Stop()
			wstunsrv.Stop()
			server.Close()
			VV = saved
		})
		runTests()

		Context("with a proxy", func() {
			BeforeEach(func() {
				proxyServer = httptest.NewServer(http.HandlerFunc(externalProxyServer))
				proxyURL, _ = url.Parse(proxyServer.URL)
				proxyErrorLog = ""
				proxyConnCount = 0
			})
			AfterEach(func() {
				proxyURL = nil
				proxyServer.Close()
				Ω(proxyErrorLog).Should(Equal(""))
				Ω(proxyConnCount).Should(Equal(1))
			})
			runTests()
		})
	})

	// wstunnel connected to an external http service
	Context("Basic requests", func() {
		BeforeEach(func() {
			server = ghttp.NewServer()

			l, _ := net.Listen("tcp", "127.0.0.1:0")
			wstunHost = l.Addr().String()
			wstunsrv = NewWSTunnelServer([]string{})
			wstunsrv.Start(l)
			wstunURL = "http://" + wstunHost

			log15.Info("Client started")

			startClient = func(wstunToken string, wstunHost string, proxy *url.URL, server *ghttp.Server) *WSTunnelClient {
				fmt.Printf("Starting client with token %s, and proxy %s\n", wstunToken, proxy)
				wstuncli = NewWSTunnelClient([]string{
					"-token", wstunToken,
					"-tunnel", "ws://" + wstunHost,
					"-server", server.URL(),
				})
				if err := wstuncli.Start(); err != nil {
					log15.Error("Error starting client", "error", err)
					os.Exit(1)
				}
				return wstuncli
			}

		})
		AfterEach(func() {
			wstuncli.Stop()
			wstunsrv.Stop()
			server.Close()
		})
		runTests()
	})
})
