package coreapi

import (
	corepkg "github.com/UruhaLushia/sparkle-service/core"
	"github.com/UruhaLushia/sparkle-service/route/auth"
	"github.com/UruhaLushia/sparkle-service/route/httphelper"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

var (
	cm     *corepkg.CoreManager
	isInit atomic.Bool
)

func Router() http.Handler {
	if !isInit.Load() {
		cm = corepkg.NewCoreManager(corepkg.WithTrafficMonitorPipeSDDL(trafficMonitorPipeSDDL()))
		isInit.Store(true)
	}

	r := chi.NewRouter()

	r.Get("/", coreStatus)
	r.Get("/events", coreEvents)
	r.HandleFunc("/controller", coreControllerProxy)
	r.HandleFunc("/controller/*", coreControllerProxy)
	r.Get("/profile", coreProfile)
	r.Post("/profile", coreSaveProfile)
	r.Patch("/profile", corePatchProfile)
	r.Post("/start", coreStart)
	r.Post("/stop", coreStop)
	r.Post("/restart", coreRestart)

	return r
}

func trafficMonitorPipeSDDL() string {
	if sid, ok := auth.GetKeyManager().GetAuthorizedSID(); ok {
		return "D:PAI(A;OICI;GWGR;;;" + sid + ")(A;OICI;GWGR;;;SY)"
	}
	return ""
}

func Stop() error {
	if !isInit.Load() || cm == nil {
		return nil
	}
	return cm.StopCore()
}

func coreStatus(w http.ResponseWriter, r *http.Request) {
	status, err := cm.GetProcessInfo()
	if err != nil {
		httphelper.SendError(w, err)
		return
	}
	render.JSON(w, r, status)
}

func coreProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := corepkg.LoadLaunchProfile()
	if err != nil {
		httphelper.SendError(w, err)
		return
	}
	render.JSON(w, r, profile)
}

func coreSaveProfile(w http.ResponseWriter, r *http.Request) {
	var profile corepkg.LaunchProfile
	if err := httphelper.DecodeRequest(r, &profile); err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}

	if err := corepkg.SaveLaunchProfile(profile); err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	normalized, err := corepkg.LoadLaunchProfile()
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	cm.ApplyLaunchProfile(normalized, coreLaunchOptions(r)...)

	httphelper.SendJSON(w, "success", "核心启动配置已更新")
}

func corePatchProfile(w http.ResponseWriter, r *http.Request) {
	var patch corepkg.LaunchProfilePatch
	if err := httphelper.DecodeRequest(r, &patch); err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}

	profile, err := corepkg.PatchLaunchProfile(patch)
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	cm.ApplyLaunchProfile(profile, coreLaunchOptions(r)...)

	httphelper.SendJSON(w, "success", "核心启动配置已更新")
}

func coreStart(w http.ResponseWriter, r *http.Request) {
	profile, hasProfile, err := decodeOptionalLaunchProfile(r)
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	if hasProfile {
		if err := corepkg.SaveLaunchProfile(*profile); err != nil {
			httphelper.SendError(w, httphelper.BadRequest(err.Error()))
			return
		}
	}

	if err := cm.StartCoreWithProfile(profile, coreLaunchOptions(r)...); err != nil {
		httphelper.SendError(w, err)
		return
	}

	sendCoreReady(w, r, "核心启动成功")
}

func coreStop(w http.ResponseWriter, r *http.Request) {
	if err := cm.StopCore(); err != nil {
		httphelper.SendError(w, err)
		return
	}
	httphelper.SendJSON(w, "success", "核心停止成功")
}

func coreRestart(w http.ResponseWriter, r *http.Request) {
	profile, hasProfile, err := decodeOptionalLaunchProfile(r)
	if err != nil {
		httphelper.SendError(w, httphelper.BadRequest(err.Error()))
		return
	}
	if hasProfile {
		if err := corepkg.SaveLaunchProfile(*profile); err != nil {
			httphelper.SendError(w, httphelper.BadRequest(err.Error()))
			return
		}
	}

	if err := cm.RestartCoreWithProfile(profile, coreLaunchOptions(r)...); err != nil {
		httphelper.SendError(w, err)
		return
	}
	sendCoreReady(w, r, "核心重启成功")
}

func decodeOptionalLaunchProfile(r *http.Request) (*corepkg.LaunchProfile, bool, error) {
	var profile corepkg.LaunchProfile
	ok, err := httphelper.DecodeOptionalRequest(r, &profile)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return new(profile), true, nil
}

func sendCoreReady(w http.ResponseWriter, r *http.Request, message string) {
	status, err := cm.GetProcessInfo()
	if err != nil {
		httphelper.SendJSON(w, "success", message)
		return
	}

	render.JSON(w, r, map[string]any{
		"status":  "success",
		"message": message,
		"core":    status,
	})
}
