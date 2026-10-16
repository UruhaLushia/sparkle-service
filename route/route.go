package route

import (
	"github.com/UruhaLushia/sparkle-service/route/auth"
	"github.com/UruhaLushia/sparkle-service/route/coreapi"
	"github.com/UruhaLushia/sparkle-service/route/httphelper"
	"github.com/UruhaLushia/sparkle-service/route/lanternapi"
	"github.com/UruhaLushia/sparkle-service/route/serviceapi"
	"github.com/UruhaLushia/sparkle-service/route/sysapi"
	"github.com/UruhaLushia/sparkle-service/route/sysproxyapi"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

func router() *chi.Mux {
	r := chi.NewRouter()
	r.Use(render.SetContentType(render.ContentTypeJSON))
	r.Use(httphelper.RequestLogger)

	r.Group(func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			httphelper.SendJSON(w, "success", "pong")
		})
	})

	r.Group(func(r chi.Router) {
		r.Use(auth.AuthMiddleware)
		r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			httphelper.SendJSON(w, "success", "auth success")
		})
		r.Mount("/service", serviceapi.Router())
		r.Mount("/sysproxy", sysproxyapi.Router())
		r.Mount("/core", coreapi.Router())
		r.Mount("/sys", sysapi.Router())
		r.Mount("/lantern", lanternapi.Router(filepath.Join(GetConfigDir(), "sparkle", "lantern")))
	})
	return r
}
