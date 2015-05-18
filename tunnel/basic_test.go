// Copyright (c) 2015 RightScale, Inc. - see LICENSE

package tunnel

// Omega: Alt+937

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Testing basic requests", func() {

	var server *ghttp.Server
	var wstunsrv *WSTunnelServer
	var wstuncli *WSTunnelClient
	var wstunUrl string
	var wstunToken string

	BeforeEach(func() {
		wstunToken = "test567890123456-" + strconv.Itoa(rand.Int()%1000000)
		server = ghttp.NewServer()
		fmt.Fprintf(os.Stderr, "ghttp started on %s\n", server.URL())

		l, _ := net.Listen("tcp", "127.0.0.1:0")
		wstunsrv = NewWSTunnelServer([]string{})
		wstunsrv.Start(l)
		fmt.Fprintf(os.Stderr, "Server started\n")
		wstuncli = NewWSTunnelClient([]string{
			"-token", wstunToken,
			"-tunnel", "ws://" + l.Addr().String(),
			"-server", server.URL(),
		})
		wstuncli.Start()
		wstunUrl = "http://" + l.Addr().String()
	})
	AfterEach(func() {
		wstuncli.Stop()
		wstunsrv.Stop()
		server.Close()
	})

	// Perform the test by running main() with the command line args set
	It("Responds to hello requests", func() {
		server.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/hello"),
				ghttp.RespondWith(200, `WORLD`, http.Header{"Content-Type": []string{"text/world"}}),
			),
		)

		resp, err := http.Get(wstunUrl + "/_token/" + wstunToken + "/hello")
		Ω(err).ShouldNot(HaveOccurred())
		respBody, err := ioutil.ReadAll(resp.Body)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(string(respBody)).Should(Equal("WORLD"))
		Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
		Ω(resp.StatusCode).Should(Equal(200))
	})

	It("Gets error status", func() {
		server.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/hello"),
				ghttp.RespondWith(445, `WORLD`, http.Header{"Content-Type": []string{"text/world"}}),
			),
		)

		resp, err := http.Get(wstunUrl + "/_token/" + wstunToken + "/hello")
		Ω(err).ShouldNot(HaveOccurred())
		respBody, err := ioutil.ReadAll(resp.Body)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(string(respBody)).Should(Equal("WORLD"))
		Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
		Ω(resp.StatusCode).Should(Equal(445))
	})

	It("Does 100 requests", func() {
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
			resp, err := http.Get(wstunUrl + "/_token/" + wstunToken + txt)
			Ω(err).ShouldNot(HaveOccurred())
			respBody, err := ioutil.ReadAll(resp.Body)
			Ω(err).ShouldNot(HaveOccurred())
			Ω(string(respBody)).Should(Equal(txt))
			Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
			Ω(resp.StatusCode).Should(Equal(200))
		}
	})

	It("Does many requests with random sleeps", func() {
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
					w.Write([]byte(fmt.Sprintf("/hello/%d", i)))
				}
			})

		resp := make([]*http.Response, N, N)
		err := make([]error, N, N)
		wg := sync.WaitGroup{}
		wg.Add(N)
		fmt.Fprintln(os.Stderr, "Launching N concurrent requests")
		for i := 0; i < N; i++ {
			go func(i int) {
				txt := fmt.Sprintf("/hello/%d", i)
				resp[i], err[i] = http.Get(wstunUrl + "/_token/" + wstunToken + txt)
				wg.Done()
			}(i)
		}
		wg.Wait()
		fmt.Fprintln(os.Stderr, "Evaluating the N requests")
		for i := 0; i < N; i++ {
			txt := fmt.Sprintf("/hello/%d", i)
			Ω(err[i]).ShouldNot(HaveOccurred())
			respBody, err := ioutil.ReadAll(resp[i].Body)
			Ω(err).ShouldNot(HaveOccurred())
			Ω(string(respBody)).Should(Equal(txt))
			Ω(resp[i].Header.Get("Content-Type")).Should(Equal("text/world"))
			Ω(resp[i].StatusCode).Should(Equal(200))
		}
	})

})
