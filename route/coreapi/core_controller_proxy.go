package coreapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/UruhaLushia/sparkle-service/route/httphelper"
)

type controllerTransportCache struct {
	mutex     sync.Mutex
	network   string
	address   string
	transport *http.Transport
}

var coreControllerTransport controllerTransportCache

func coreControllerProxy(w http.ResponseWriter, r *http.Request) {
	network, address, err := cm.ControllerEndpoint()
	if err != nil {
		httphelper.SendError(w, httphelper.ServiceUnavailable(err.Error()))
		return
	}

	targetPath := stripCoreControllerPrefix(r.URL.Path)
	transport := coreControllerTransport.get(network, address)
	defer coreControllerTransport.closeIfStale(transport)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			req := proxyRequest.Out
			req.URL.Scheme = "http"
			req.URL.Host = "localhost"
			req.URL.Path = targetPath
			req.URL.RawPath = ""
			req.Host = "localhost"
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			httphelper.SendError(w, fmt.Errorf("转发核心控制器请求失败：%w", err))
		},
	}

	proxy.ServeHTTP(w, r)
}

func (c *controllerTransportCache) get(network, address string) *http.Transport {
	c.mutex.Lock()
	if c.transport != nil && c.network == network && c.address == address {
		transport := c.transport
		c.mutex.Unlock()
		return transport
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialCoreController(ctx, network, address)
		},
	}
	oldTransport := c.transport
	c.network = network
	c.address = address
	c.transport = transport
	c.mutex.Unlock()

	if oldTransport != nil {
		oldTransport.CloseIdleConnections()
	}
	return transport
}

func (c *controllerTransportCache) closeIfStale(transport *http.Transport) {
	c.mutex.Lock()
	stale := c.transport != transport
	c.mutex.Unlock()
	if stale {
		transport.CloseIdleConnections()
	}
}

func stripCoreControllerPrefix(path string) string {
	for _, prefix := range []string{"/core/controller", "/controller"} {
		if after, ok := strings.CutPrefix(path, prefix); ok {
			if after == "" {
				return "/"
			}
			return after
		}
	}
	return path
}
