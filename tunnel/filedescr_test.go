// Copyright (c) 2015 RightScale, Inc. - see LICENSE

package tunnel

// Omega: Alt+937

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/inconshreveable/log15"
)

var _ = Describe("Check against file descriptor leakage", func() {

	var server *ghttp.Server
	var wstunsrv *WSTunnelServer
	var wstuncli *WSTunnelClient
	var wstunUrl string
	var wstunToken string

	// hack copied from https://groups.google.com/forum/#!topic/golang-nuts/c0AnWXjzNIA
	countOpenFiles := func() int {
		out, err := exec.Command("/bin/sh", "-c",
			fmt.Sprintf("lsof -p %v", os.Getpid())).Output()
		if err != nil {
			log.Fatal(err)
		}
		lines := bytes.Count(out, []byte("\n"))
		return lines - 1
	}

	BeforeEach(func() {
		wstunToken = "test567890123456-" + strconv.Itoa(rand.Int()%1000000)
		server = ghttp.NewServer()
		log15.Info("ghttp started", "url", server.URL())

		l, _ := net.Listen("tcp", "127.0.0.1:0")
		wstunsrv = NewWSTunnelServer([]string{})
		wstunsrv.Start(l)
		wstuncli = NewWSTunnelClient([]string{
			"-token", wstunToken,
			"-tunnel", "ws://" + l.Addr().String(),
			"-server", server.URL(),
			"-insecure",
		})
		wstuncli.Start()
		wstunUrl = "http://" + l.Addr().String()
	})
	AfterEach(func() {
		wstuncli.Stop()
		wstunsrv.Stop()
		server.Close()
	})

	It("Does not leak FDs after 100 requests", func() {
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

		startFd := countOpenFiles()
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
		endFd := countOpenFiles()
		log15.Info("file descriptors", "startFd", startFd, "endFd", endFd)
		Ω(endFd - startFd).Should(BeNumerically("<", 10))
	})

})
