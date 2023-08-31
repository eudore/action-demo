package main

/*
eudore.App对象的简单组装各类对象，实现Value/SetValue、Listen和Run方法。
*/

import (
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/eudore/eudore"
	_ "github.com/eudore/eudore/daemon"
	"github.com/eudore/eudore/middleware"
)

func main() {
	eudore.DefaultLoggerFormatterFormatTime = "none"
	app := eudore.NewApp()
	defer app.Run()
	if app.Parse() != nil {
		return
	}

	addr, _ := url.Parse("http://127.0.0.1:30000/")
	proxy := httputil.NewSingleHostReverseProxy(addr)
	app.AddMiddleware("global", func(ctx eudore.Context) {
		req := ctx.Request()
		if strings.Contains(req.Host, ":8086") {
			req.Host = "godoc.kube-public.eudore.cn:30000"
			proxy.ServeHTTP(ctx.Response(), req)
			ctx.End()
		}
	})

	app.SetValue(eudore.ContextKeyHandlerExtender, eudore.NewHandlerExtender())
	app.SetValue(eudore.ContextKeyFuncCreator, eudore.NewFuncCreatorExpr())
	app.AddMiddleware(
		middleware.NewHeaderFilteFunc(nil, nil),
		middleware.NewRecoverFunc(),
		middleware.NewLoggerFunc(app, "route"),
		middleware.NewBasicAuthFunc(map[string]string{"eudore": "11"}),
		middleware.NewCacheFunc(),
		middleware.NewCompressMixinsFunc(nil),
		middleware.NewDumpFunc(app.Group("/eudore/debug")),
	)
	app.AddHandler("404", "", eudore.HandlerRouter404)
	app.GetFunc("/bind", func(ctx eudore.Context) {
		type Config struct {
			Name []int
		}
		c := &Config{}
		ctx.Bind(c)
		ctx.Render(c)
	})
	app.GetFunc("/fatal", func(ctx eudore.Context) {
		ctx.Fatal(3)
	})

	app.AddController(&eudore.ControllerAutoRoute{})
	debug := app.Group("/eudore/debug")
	debug.GetFunc("/admin/ui", middleware.HandlerAdmin)
	debug.AnyFunc("/pprof/*", middleware.HandlerPprof)
	debug.GetFunc("/look/*", middleware.NewLookFunc(app))
	debug.GetFunc("/meta/*", eudore.HandlerMetadata)
	debug.GetFunc("/src/* autoindex=true", eudore.NewHandlerStatic("."))
	debug.GetFunc("/panic", func(ctx eudore.Context) {
		panic(ctx.Path())
	})

	app.Listen(":8086")
	app.Listen(":8087")

	app.GetFunc("/values", func(ctx eudore.Context) {
		ctx.Debug(ctx.GetHeader(eudore.HeaderContentType))
		ctx.FormValue("name")
		ctx.WriteString("string")
	})
	app.GetFunc("/text", func(ctx eudore.Context) {
		ctx.Render("name")
	})
	app.GetFunc("/log", func(ctx eudore.Context) {
		ctx.FormFiles()
		ctx.Info("info1")
		ctx.WithField("l", 2).Info("info2")
		ctx.WithField("depth", "stack").Info("info2")
	})
	client := app.WithClient(
		eudore.NewClientOptionBasicauth("eudore", "11"),
		&eudore.ClientTrace{},
		time.Second*3,
	)
	client.NewRequest(nil, "GET", "/values?d=1")

	app.Warning("-----------------")
	client.NewRequest(nil, "GET", "/log?d=1")
	app.Info("info")
	app.AddHandlerExtend(999)
	app.AddHandlerExtend(eudore.NewHandlerStringer)
	app.Router.AddMiddleware(eudore.HandlerEmpty)
	app.AddMiddleware(eudore.HandlerEmpty)
	app.AddController(myController{})
	app.GetFunc("/xx", eudore.HandlerEmpty)
	app.Warning("-----------------")

	// app.CancelFunc()
}

type myController struct {
	eudore.ControllerAutoRoute
}

func (myController) Get() {

}
