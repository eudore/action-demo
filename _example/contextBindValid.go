package main

/*
设置参数valid非空时，调用Bind后执行Validate.
*/

import (
	"github.com/eudore/eudore"
	"github.com/eudore/eudore/component/httptest"
)

type userRequest struct {
	Username string `validate:"regexp:^[a-zA-Z]*$"`
	Name     string `validate:"nozero"`
	Age      int    `validate:"min:21,max:40"`
	Password string `validate:"len:>7"`
}

func main() {
	app := eudore.NewApp()
	app.SetValue(eudore.ContextKeyValidate, eudore.NewValidateField(app))
	app.SetValue(eudore.ContextKeyContextPool, eudore.NewContextBasePool(app))
	// 上传文件信息
	app.PutFunc("/file/data/:path", func(ctx eudore.Context) {
		var user userRequest
		ctx.Bind(&user)
	})

	app.PutFunc("/file/data/1", func(ctx eudore.Context) {
		var user userRequest
		ctx.Bind(&user)
	})

	client := httptest.NewClient(app)
	client.NewRequest("PUT", "/file/data/1").WithHeaderValue("Content-Type", "application/json").WithBodyString(`{"username":"abc","name":"eudore","age":21,"password":"12345678"}`).Do().CheckStatus(200).Out()
	client.NewRequest("PUT", "/file/data/1").WithHeaderValue("Content-Type", "application/json").WithBodyString(`{"username":"abc","name":"","age":21,"password":"12345"}`).Do().CheckStatus(200).Out()
	client.NewRequest("PUT", "/file/data/2").WithHeaderValue("Content-Type", "application/json").WithBodyString(`{"username":"abc","name":"","age":21,"password":"12345"}`).Do().CheckStatus(200).Out()

	app.Listen(":8088")
	app.Run()
}
