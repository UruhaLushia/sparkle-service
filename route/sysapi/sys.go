package sysapi

import (
	"fmt"
	"github.com/UruhaLushia/sparkle-service/route/httphelper"
	"github.com/UruhaLushia/sparkle-service/sys"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

type dnsRequest struct {
	Device  string   `json:"device,omitempty"`
	Servers []string `json:"servers,omitempty"`
}

func Router() http.Handler {
	r := chi.NewRouter()

	r.Get("/cpu", cpuInfo)
	r.Post("/dns/set", setDns)

	return r
}

func cpuInfo(w http.ResponseWriter, r *http.Request) {
	info, err := sys.GetCPUInfo()
	if err != nil {
		httphelper.SendError(w, err)
		return
	}
	render.JSON(w, r, info)
}

func setDns(w http.ResponseWriter, r *http.Request) {
	var req dnsRequest
	if err := httphelper.DecodeRequest(r, &req); err != nil {
		httphelper.SendError(w, httphelper.BadRequest(fmt.Sprintf("无效的请求体: %v", err)))
		return
	}
	if err := sys.SetDns(req.Device, req.Servers); err != nil {
		httphelper.SendError(w, err)
		return
	}
	httphelper.SendJSON(w, "success", "DNS 设置成功")
}
