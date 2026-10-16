package lanternapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/UruhaLushia/lantern"
	"github.com/UruhaLushia/lantern/announce"
	"github.com/UruhaLushia/lantern/device"
	"github.com/UruhaLushia/lantern/dhcpserver"
	"github.com/UruhaLushia/lantern/discovery"
	"github.com/UruhaLushia/lantern/network"
	"github.com/UruhaLushia/sparkle-service/route/httphelper"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

type startOptions struct {
	GatewayMode bool               `json:"gateway_mode"`
	Interface   string             `json:"interface"`
	GatewayIPv4 netip.Addr         `json:"gateway_ipv4,omitzero"`
	GatewayIPv6 netip.Addr         `json:"gateway_ipv6,omitzero"`
	Discovery   discovery.Options  `json:"discovery"`
	RA          announce.Options   `json:"ra"`
	DHCP        dhcpserver.Options `json:"dhcp"`
}

type snapshot struct {
	Supported   bool                 `json:"supported"`
	Running     bool                 `json:"running"`
	GatewayMode bool                 `json:"gateway_mode"`
	Probe       *discovery.DHCPProbe `json:"probe,omitempty"`
	Options     *startOptions        `json:"options,omitempty"`
	Status      lantern.Status       `json:"status"`
	Devices     []device.Device      `json:"devices"`
}

type manager struct {
	mu                sync.Mutex
	controller        *lantern.Controller
	options           *startOptions
	closed            bool
	probing           bool
	probeStop         context.CancelFunc
	lastProbe         *discovery.DHCPProbe
	store             *device.Store
	forwarding        *forwardingState
	forwardingUpdates []*forwardingState
}

var controllerManager manager

func Router(stateDir string) http.Handler {
	r := chi.NewRouter()
	r.Get("/", controllerManager.status)
	r.Get("/interfaces", func(w http.ResponseWriter, r *http.Request) {
		v, err := network.Discover()
		if err != nil {
			httphelper.SendError(w, err)
			return
		}
		render.JSON(w, r, v)
	})
	r.Post("/start", func(w http.ResponseWriter, r *http.Request) { controllerManager.start(w, r, stateDir) })
	r.Post("/stop", controllerManager.stop)
	r.Put("/config", func(w http.ResponseWriter, r *http.Request) { controllerManager.configure(w, r, stateDir) })
	r.Post("/probe", controllerManager.probe)
	r.Post("/dhcp/resume", controllerManager.resumeDHCP)
	r.Put("/dhcp/gateway-devices", controllerManager.gatewayDevices)
	return r
}

func Shutdown() error {
	m := &controllerManager
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.probeStop != nil {
		m.probeStop()
	}
	return m.stopLocked()
}

func (m *manager) runningLocked() bool {
	if m.controller == nil {
		return false
	}
	select {
	case <-m.controller.Done():
		return false
	default:
		return true
	}
}

func (m *manager) snapshotLocked() snapshot {
	v := snapshot{Supported: runtime.GOOS == "linux", Running: m.runningLocked(), Options: m.options,
		Probe: m.lastProbe, Status: lantern.Status{State: "idle"}, Devices: []device.Device{}}
	v.GatewayMode = v.Running && m.options != nil && m.options.GatewayMode
	if m.controller != nil {
		v.Status, v.Devices = m.controller.Status(), m.controller.Devices()
	}
	return v
}

func (m *manager) status(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	render.JSON(w, r, m.snapshotLocked())
}

func (m *manager) start(w http.ResponseWriter, r *http.Request, stateDir string) {
	if runtime.GOOS != "linux" {
		httphelper.SendError(w, httphelper.NewError(http.StatusNotImplemented, "局域网网关暂仅支持 Linux"))
		return
	}
	var opts startOptions
	if err := decode(w, r, &opts); err != nil {
		httphelper.SendError(w, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.probing || (m.runningLocked() && m.options.GatewayMode && opts.GatewayMode) {
		httphelper.SendError(w, httphelper.Conflict("网关已启动、DHCP 探测进行中或服务正在退出"))
		return
	}
	opts.Discovery.Enabled = true
	active := opts
	var requireRA bool
	var forwardIPv6 bool
	if opts.GatewayMode {
		netState, err := network.Discover()
		if err != nil {
			httphelper.SendError(w, err)
			return
		}
		selected, err := netState.Select(opts.Interface, opts.GatewayIPv4, opts.GatewayIPv6)
		if err == nil && !selected.GatewayIPv4.IsValid() {
			err = errors.New("未识别到 IPv4 主路由，请检查接口或手动指定")
		}
		if err != nil {
			httphelper.SendError(w, httphelper.Conflict(err.Error()))
			return
		}
		opts.Interface = selected.Interface.Name
		probe, err := discovery.ProbeDHCP(r.Context(), opts.Interface, 3*time.Second)
		m.lastProbe = &probe
		if err != nil {
			httphelper.SendError(w, httphelper.Conflict("DHCP 探测失败，禁止启用网关模式："+err.Error()))
			return
		}
		if err := allowGateway(probe); err != nil {
			httphelper.SendError(w, err)
			return
		}
		opts.DHCP.Enabled = true
		requireRA = opts.RA.Enabled && selected.GatewayIPv6.IsValid()
		forwardIPv6 = opts.RA.Enabled || selected.GatewayIPv6.IsValid()
		opts.DHCP.Gateway = selected.GatewayIPv4
		opts.DHCP.LeaseFile = filepath.Join(stateDir, "leases.json")
		opts.DHCP, err = opts.DHCP.Normalize()
		if err != nil {
			httphelper.SendError(w, httphelper.BadRequest(err.Error()))
			return
		}
		active = opts
	} else {
		active.RA = announce.Options{}
		active.DHCP = dhcpserver.Options{}
	}
	if m.store == nil {
		m.store = device.NewStore(4096)
	}
	c, err := lantern.New(lantern.Options{Interface: active.Interface, GatewayIPv4: active.GatewayIPv4,
		GatewayIPv6: active.GatewayIPv6, PollInterval: 2 * time.Second, Discovery: active.Discovery,
		RA: active.RA, DHCP: active.DHCP, DeviceStore: m.store})
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	if err := r.Context().Err(); err != nil {
		httphelper.SendError(w, err)
		return
	}
	if err := m.stopLocked(); err != nil {
		httphelper.SendError(w, err)
		return
	}
	if opts.GatewayMode {
		m.forwarding, err = prepareForwarding(opts.Interface, forwardIPv6)
		if err != nil {
			httphelper.SendError(w, err)
			return
		}
	}
	if err := c.Start(context.Background()); err != nil {
		httphelper.SendError(w, errors.Join(err, m.stopLocked()))
		return
	}
	m.controller, m.options = c, &opts
	if opts.GatewayMode {
		status := c.Status()
		if !status.Discovery.Running || (requireRA && !status.RA.Enabled) || !status.DHCP.Running {
			cleanupErr := m.stopLocked()
			httphelper.SendError(w, errors.Join(fmt.Errorf("网关组件启动不完整，已撤销启动：%v", status.Problems), cleanupErr))
			return
		}
	}
	render.JSON(w, r, m.snapshotLocked())
}

func (m *manager) stopLocked() error {
	var stopErr error
	if m.controller != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.controller.Stop(ctx); err != nil {
			if m.runningLocked() {
				return err
			}
			stopErr = err
		}
	}
	if m.forwarding != nil {
		for len(m.forwardingUpdates) > 0 {
			i := len(m.forwardingUpdates) - 1
			if err := m.forwardingUpdates[i].restore(); err != nil {
				return errors.Join(stopErr, err)
			}
			m.forwardingUpdates = m.forwardingUpdates[:i]
		}
		if err := m.forwarding.restore(); err != nil {
			return errors.Join(stopErr, err)
		}
		m.forwarding = nil
	}
	return stopErr
}

func (m *manager) configure(w http.ResponseWriter, r *http.Request, stateDir string) {
	var opts startOptions
	if err := decode(w, r, &opts); err != nil {
		httphelper.SendError(w, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.probing || !m.runningLocked() || m.options == nil || !m.options.GatewayMode {
		httphelper.SendError(w, httphelper.Conflict("网关模式未开启"))
		return
	}
	netState, err := network.Discover()
	if err != nil {
		httphelper.SendError(w, err)
		return
	}
	selected, err := netState.Select(opts.Interface, opts.GatewayIPv4, opts.GatewayIPv6)
	if err == nil && !selected.GatewayIPv4.IsValid() {
		err = errors.New("未识别到 IPv4 主路由，请检查接口或手动指定")
	}
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	if selected.Interface.Name != m.options.Interface {
		probe, err := discovery.ProbeDHCP(r.Context(), selected.Interface.Name, 3*time.Second)
		m.lastProbe = &probe
		if err == nil {
			err = allowGateway(probe)
		}
		if err != nil {
			httphelper.SendError(w, httphelper.Conflict("切换接口前 DHCP 探测未通过："+err.Error()))
			return
		}
	}
	opts.GatewayMode, opts.Discovery = true, m.options.Discovery
	opts.Interface = selected.Interface.Name
	opts.DHCP.Enabled, opts.DHCP.Gateway, opts.DHCP.LeaseFile = true, selected.GatewayIPv4, filepath.Join(stateDir, "leases.json")
	opts.DHCP.GatewayMACs = m.options.DHCP.GatewayMACs
	opts.DHCP, err = opts.DHCP.Normalize()
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	var forwarding *forwardingState
	ipv6 := opts.RA.Enabled || selected.GatewayIPv6.IsValid()
	if network.ForwardingReady(opts.Interface, true, ipv6) != nil {
		forwarding, err = prepareForwarding(opts.Interface, ipv6)
		if err != nil {
			if forwarding != nil {
				m.forwardingUpdates = append(m.forwardingUpdates, forwarding)
			}
			httphelper.SendError(w, err)
			return
		}
	}
	err = m.controller.ConfigureGateway(lantern.GatewayOptions{Interface: opts.Interface,
		GatewayIPv4: opts.GatewayIPv4, GatewayIPv6: opts.GatewayIPv6, RA: opts.RA, DHCP: opts.DHCP})
	if err != nil {
		if forwarding != nil {
			if restoreErr := forwarding.restore(); restoreErr != nil {
				m.forwardingUpdates = append(m.forwardingUpdates, forwarding)
				err = errors.Join(err, restoreErr)
			}
		}
		httphelper.SendError(w, err)
		return
	}
	if forwarding != nil {
		m.forwardingUpdates = append(m.forwardingUpdates, forwarding)
	}
	m.options = &opts
	render.JSON(w, r, m.snapshotLocked())
}

func (m *manager) stop(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.stopLocked(); err != nil {
		httphelper.SendError(w, err)
		return
	}
	render.JSON(w, r, m.snapshotLocked())
}

func (m *manager) probe(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Interface string `json:"interface"`
	}
	if err := decode(w, r, &request); err != nil {
		httphelper.SendError(w, err)
		return
	}
	m.mu.Lock()
	if m.closed || m.probing {
		m.mu.Unlock()
		httphelper.SendError(w, httphelper.Conflict("服务正在退出或 DHCP 探测进行中"))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	m.probing, m.probeStop = true, cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		m.probing, m.probeStop = false, nil
		m.mu.Unlock()
	}()
	result, err := discovery.ProbeDHCP(ctx, request.Interface, 3*time.Second)
	m.mu.Lock()
	m.lastProbe = &result
	m.mu.Unlock()
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	render.JSON(w, r, result)
}

func (m *manager) resumeDHCP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.runningLocked() {
		httphelper.SendError(w, httphelper.Conflict("网关未运行"))
		return
	}
	if m.options == nil || !m.options.GatewayMode {
		httphelper.SendError(w, httphelper.Conflict("网关模式未开启"))
		return
	}
	probe, err := discovery.ProbeDHCP(r.Context(), m.options.Interface, 3*time.Second)
	m.lastProbe = &probe
	if err != nil {
		httphelper.SendError(w, httphelper.Conflict("DHCP 探测失败："+err.Error()))
		return
	}
	if err := allowGateway(probe); err != nil {
		httphelper.SendError(w, err)
		return
	}
	if err := m.controller.ResumeDHCP(); err != nil {
		httphelper.SendError(w, httphelper.Conflict(err.Error()))
		return
	}
	render.JSON(w, r, m.snapshotLocked())
}

func (m *manager) gatewayDevices(w http.ResponseWriter, r *http.Request) {
	var macs []string
	if err := decode(w, r, &macs); err != nil {
		httphelper.SendError(w, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.runningLocked() || m.options == nil || !m.options.GatewayMode {
		httphelper.SendError(w, httphelper.Conflict("网关模式未开启"))
		return
	}
	opts := m.options.DHCP
	opts.GatewayMACs = macs
	opts, err := opts.Normalize()
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	if err := m.controller.SetDHCPGatewayMACs(opts.GatewayMACs); err != nil {
		httphelper.SendError(w, err)
		return
	}
	m.options.DHCP.GatewayMACs = m.controller.Status().DHCP.GatewayMACs
	render.JSON(w, r, m.snapshotLocked())
}

func allowGateway(probe discovery.DHCPProbe) error {
	if probe.Truncated || probe.DiscoveriesSent == 0 {
		return httphelper.Conflict("DHCP 探测未完整完成，禁止启用网关模式")
	}
	if len(probe.Servers) != 0 {
		servers := make([]string, 0, len(probe.Servers))
		for _, server := range probe.Servers {
			servers = append(servers, fmt.Sprintf("%s（%s）", server.Address, server.MAC))
		}
		return httphelper.Conflict(fmt.Sprintf("发现 DHCP 服务器 %v，请关闭对应 DHCP 服务后重新探测；网关模式未启用", servers))
	}
	return nil
}

func decode(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return httphelper.BadRequest(err.Error())
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return httphelper.BadRequest("请求必须包含一个 JSON 值")
	}
	return nil
}
