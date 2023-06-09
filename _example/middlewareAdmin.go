package main

/*
admin ui实现各组件api的web操作。
访问 http://127.0.0.1:8088/eudore/debug/admin/ui
*/

import (
	"github.com/eudore/eudore"
	"github.com/eudore/eudore/component/httptest"
	"github.com/eudore/eudore/middleware"
)

func main() {
	app := eudore.NewApp()
	app.SetValue(eudore.ContextKeyRouter, eudore.NewRouterStd(eudore.NewRouterCoreDebug(nil)))
	app.SetValue(eudore.ContextKeyRender, eudore.RenderJSON)
	app.SetValue(eudore.ContextKeyContextPool, eudore.NewContextBasePool(app))

	admin := app.Group("/eudore/debug")
	admin.AddMiddleware(middleware.NewCorsFunc(nil, nil))
	//	admin.AddMiddleware(middleware.NewBasicAuthFunc(map[string]string{"user": "pw"}))
	admin.AddController(middleware.NewPprofController())
	admin.AnyFunc("/look/*", middleware.NewLookFunc(app))

	app.AddMiddleware(middleware.NewLoggerFunc(app, "route"))
	app.AddMiddleware(middleware.NewDumpFunc(admin))
	app.AddMiddleware(middleware.NewBlackFunc(map[string]bool{"0.0.0.0/0": true, "10.0.0.0/8": false}, admin))
	app.AddMiddleware(middleware.NewHeaderWithSecureFunc(nil))
	app.AddMiddleware(middleware.NewCorsFunc(nil, map[string]string{
		"Access-Control-Allow-Credentials": "true",
		"Access-Control-Allow-Headers":     "Authorization,DNT,X-CustomHeader,Keep-Alive,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,X-Parent-Id",
		"Access-Control-Expose-Headers":    "X-Request-Id",
		"access-control-allow-methods":     "GET, POST, PUT, DELETE, HEAD",
		"access-control-max-age":           "1000",
	}))
	app.AddMiddleware(middleware.NewBreakerFunc(admin))
	app.AddHandler("404", "", eudore.HandlerRouter404)
	app.AddHandler("405", "", eudore.HandlerRouter405)
	app.AnyFunc("/echo", func(ctx eudore.Context) {
		ctx.Write(ctx.Body())
	})
	// 注册admin ui处理
	app.AnyFunc("/eudore/debug/admin/ui", middleware.HandlerAdmin)
	app.AnyFunc("/", func(ctx eudore.Context) {
		ctx.Redirect(301, "/eudore/debug/admin/ui")
	})

	client := httptest.NewClient(app)
	client.NewRequest("GET", "/1").Do()
	client.NewRequest("GET", "/eudore/debug/admin/ui").Do()

	app.Listen(":8088")
	app.ListenTLS(":8089", "", "")
	// app.CancelFunc()
	app.Run()
}
