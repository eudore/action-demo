package eudore

import (
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"unsafe"
)

// HandlerFunc 是处理一个Context的函数
type HandlerFunc func(Context)

// HandlerFuncs 是HandlerFunc的集合，表示多个请求处理函数。
type HandlerFuncs []HandlerFunc

// HandlerExtender 定义函数扩展处理者的方法。
//
// HandlerExtender默认拥有Base、Warp、Tree三种实现，具体参数三种对象的文档。
type HandlerExtender interface {
	RegisterHandlerExtend(string, interface{}) error
	NewHandlerFuncs(string, interface{}) HandlerFuncs
	ListExtendHandlerNames() []string
}

// handlerExtendBase 定义基础的函数扩展。
type handlerExtendBase struct {
	ExtendNewType       []reflect.Type
	ExtendNewFunc       []reflect.Value
	ExtendInterfaceType []reflect.Type
	ExtendInterfaceFunc []reflect.Value
}

// handlerExtendWarp 定义链式函数扩展。
type handlerExtendWarp struct {
	HandlerExtender
	LastExtender HandlerExtender
}

// handlerExtendTree 定义基于路径匹配的函数扩展。
type handlerExtendTree struct {
	HandlerExtender
	path   string
	childs []*handlerExtendTree
}

type handlerHTTP interface {
	HandleHTTP(Context)
}

var (
	// contextFuncName key类型一定为HandlerFunc类型，保存函数可能正确的名称。
	contextFuncName    = make(map[uintptr]string)   // 最终名称
	contextSaveName    = make(map[uintptr]string)   // 函数名称
	contextAliasName   = make(map[uintptr][]string) // 对象名称
	fineLineFieldsKeys = []string{"file", "line"}
)

// init 函数初始化内置扩展的请求上下文处理函数。
func init() {
	// 路由方法扩展
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendHandlerHTTP)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendHandlerNetHTTP)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncNetHTTP1)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncNetHTTP2)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFunc)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncRender)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncError)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncRenderError)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncContextError)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncContextRender)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncContextRenderError)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncContextInterfaceError)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncRPCMap)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendHandlerRPC)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendFuncString)
	DefaultHandlerExtend.RegisterHandlerExtend("", NewExtendHandlerStringer)
}

// NewHandlerExtendBase method returns a basic function extension processing object.
//
// The NewHandlerExtendBase().RegisterHandlerExtend method registers a conversion function and ignores the path.
//
// The NewHandlerExtendBase().NewHandlerFuncs method implementation creates multiple request handler functions, ignoring paths.
//
// NewHandlerExtendBase 方法返回一个基本的函数扩展处理对象。
//
// NewHandlerExtendBase().RegisterHandlerExtend 方法实现注册一个转换函数，忽略路径。
//
// NewHandlerExtendBase().NewHandlerFuncs 方法实现创建多个请求处理函数，忽略路径。
func NewHandlerExtendBase() HandlerExtender {
	return &handlerExtendBase{}
}

// RegisterHandlerExtend 函数注册一个请求上下文处理转换函数，参数必须是一个函数，该函数的参数必须是一个函数、接口、指针类型之一，返回值必须是返回一个HandlerFunc对象。
//
// 如果添加多个接口类型转换，注册类型不直接是接口而是实现接口，会按照接口注册顺序依次检测是否实现接口。
//
// 例如: func(func(...)) HanderFunc, func(http.Handler) HandlerFunc
func (ext *handlerExtendBase) RegisterHandlerExtend(_ string, fn interface{}) error {
	iType := reflect.TypeOf(fn)
	// RegisterHandlerExtend函数的参数必须是一个函数类型
	if iType.Kind() != reflect.Func {
		return ErrRegisterNewHandlerParamNotFunc
	}

	// 检查函数参数必须为 func(Type) 或 func(string, Type) ,允许使用的type值定义在DefaultHandlerExtendAllowType。
	if (iType.NumIn() != 1) && (iType.NumIn() != 2 || iType.In(0).Kind() != reflect.String) {
		return fmt.Errorf(ErrFormatRegisterHandlerExtendInputParamError, iType.String())
	}
	_, ok := DefaultHandlerExtendAllowType[iType.In(iType.NumIn()-1).Kind()]
	if !ok {
		return fmt.Errorf(ErrFormatRegisterHandlerExtendInputParamError, iType.String())
	}

	// 检查函数返回值必须是HandlerFunc
	if iType.NumOut() != 1 || iType.Out(0) != typeHandlerFunc {
		return fmt.Errorf(ErrFormatRegisterHandlerExtendOutputParamError, iType.String())
	}

	ext.ExtendNewType = append(ext.ExtendNewType, iType.In(iType.NumIn()-1))
	ext.ExtendNewFunc = append(ext.ExtendNewFunc, reflect.ValueOf(fn))
	if iType.In(iType.NumIn()-1).Kind() == reflect.Interface {
		ext.ExtendInterfaceType = append(ext.ExtendInterfaceType, iType.In(iType.NumIn()-1))
		ext.ExtendInterfaceFunc = append(ext.ExtendInterfaceFunc, reflect.ValueOf(fn))
	}
	return nil
}

// NewHandlerFuncs 函数根据参数返回一个HandlerFuncs。
func (ext *handlerExtendBase) NewHandlerFuncs(path string, i interface{}) HandlerFuncs {
	val, ok := i.(reflect.Value)
	if !ok {
		val = reflect.ValueOf(i)
	}
	return NewHandlerFuncsFilter(ext.newHandlerFuncs(path, val))
}

func (ext *handlerExtendBase) newHandlerFuncs(path string, iValue reflect.Value) HandlerFuncs {
	// 基础类型返回
	switch fn := iValue.Interface().(type) {
	case func(Context):
		SetHandlerFuncName(fn, getHandlerAliasName(iValue))
		return HandlerFuncs{fn}
	case HandlerFunc:
		SetHandlerFuncName(fn, getHandlerAliasName(iValue))
		return HandlerFuncs{fn}
	case []HandlerFunc:
		return fn
	case HandlerFuncs:
		return fn
	}
	// 尝试转换成HandlerFuncs
	fn := ext.newHandlerFunc(path, iValue)
	if fn != nil {
		return HandlerFuncs{fn}
	}
	// 解引用数组再转换HandlerFuncs
	switch iValue.Type().Kind() {
	case reflect.Slice, reflect.Array:
		var fns HandlerFuncs
		for i := 0; i < iValue.Len(); i++ {
			hs := ext.newHandlerFuncs(path, iValue.Index(i))
			if hs != nil {
				fns = append(fns, hs...)
			}
		}
		if len(fns) != 0 {
			return fns
		}
	case reflect.Interface, reflect.Ptr:
		return ext.newHandlerFuncs(path, iValue.Elem())
	}
	return nil
}

// newHandlerFunc 函数使用一个函数或接口参数转换成请求上下文处理函数。
//
// 参数必须是一个函数，函数拥有一个参数作为入参，一个HandlerFunc对象作为返回值。
//
// 先检测对象是否拥有直接注册的类型扩展函数，再检查对象是否实现其中注册的接口类型。
//
// 允许进行多次注册，只要注册返回值不为空就会返回对应的处理函数。
func (ext *handlerExtendBase) newHandlerFunc(path string, iValue reflect.Value) HandlerFunc {
	iType := iValue.Type()
	for i := range ext.ExtendNewType {
		if ext.ExtendNewType[i] == iType {
			h := ext.createHandlerFunc(path, ext.ExtendNewFunc[i], iValue)
			if h != nil {
				return h
			}
		}
	}
	// 判断是否实现接口类型
	for i, iface := range ext.ExtendInterfaceType {
		if iType.Implements(iface) {
			h := ext.createHandlerFunc(path, ext.ExtendInterfaceFunc[i], iValue)
			if h != nil {
				return h
			}
		}
	}
	return nil
}

// createHandlerFunc 函数使用转换函数和对象创建一个HandlerFunc，并保存HandlerFunc的名称和使用的扩展函数名称。
func (ext *handlerExtendBase) createHandlerFunc(path string, fn, iValue reflect.Value) (h HandlerFunc) {
	if fn.Type().NumIn() == 1 {
		h = fn.Call([]reflect.Value{iValue})[0].Interface().(HandlerFunc)
	} else {
		h = fn.Call([]reflect.Value{reflect.ValueOf(path), iValue})[0].Interface().(HandlerFunc)
	}
	if h == nil {
		return nil
	}
	// 获取扩展名称，eudore包移除包前缀
	extname := runtime.FuncForPC(fn.Pointer()).Name()
	if len(extname) > 24 && extname[:25] == "github.com/eudore/eudore." {
		extname = extname[25:]
	}
	// 获取新函数名称,一般来源于函数扩展返回的函数名称。
	hptr := getFuncPointer(reflect.ValueOf(h))
	name := contextSaveName[hptr]
	// 使用原值名称
	if name == "" && iValue.Kind() != reflect.Struct {
		name = getHandlerAliasName(iValue)
	}
	// 推断名称
	if name == "" {
		iType := iValue.Type()
		switch iType.Kind() {
		case reflect.Func:
			name = runtime.FuncForPC(iValue.Pointer()).Name()
		case reflect.Ptr:
			iType = iType.Elem()
			name = fmt.Sprintf("*%s.%s", iType.PkgPath(), iType.Name())
		case reflect.Struct:
			name = fmt.Sprintf("%s.%s", iType.PkgPath(), iType.Name())
		}
	}
	contextFuncName[hptr] = fmt.Sprintf("%s(%s)", name, extname)
	return h
}

var formarExtendername = "%s(%s)"

// ListExtendHandlerNames 方法返回全部注册的函数名称。
func (ext *handlerExtendBase) ListExtendHandlerNames() []string {
	names := make([]string, 0, len(ext.ExtendNewFunc))
	for i := range ext.ExtendNewType {
		if ext.ExtendNewType[i].Kind() != reflect.Interface {
			names = append(names, fmt.Sprintf(formarExtendername, runtime.FuncForPC(ext.ExtendNewFunc[i].Pointer()).Name(), ext.ExtendNewType[i].String()))
		}
	}
	for i, iface := range ext.ExtendInterfaceType {
		names = append(names, fmt.Sprintf(formarExtendername, runtime.FuncForPC(ext.ExtendInterfaceFunc[i].Pointer()).Name(), iface.String()))
	}
	return names
}

// NewHandlerExtendWarp function creates a chained HandlerExtender object.
//
// All objects are registered and created using base. If base cannot create a function handler, use last to create a function handler.
//
// NewHandlerExtendWarp 函数创建一个链式HandlerExtender对象。
//
// The NewHandlerExtendWarp(base, last).RegisterHandlerExtend method uses the base object to register extension functions.
//
// The NewHandlerExtendWarp(base, last).NewHandlerFuncs method first uses the base object to create multiple request processing functions. If it returns nil, it uses the last object to create multiple request processing functions.
//
// 所有对象注册和创建均使用base，如果base无法创建函数处理者则使用last创建函数处理者。
//
// NewHandlerExtendWarp(base, last).RegisterHandlerExtend 方法使用base对象注册扩展函数。
//
// NewHandlerExtendWarp(base, last).NewHandlerFuncs 方法先使用base对象创建多个请求处理函数，如果返回nil，则使用last对象创建多个请求处理函数。
func NewHandlerExtendWarp(base, last HandlerExtender) HandlerExtender {
	return &handlerExtendWarp{
		HandlerExtender: base,
		LastExtender:    last,
	}
}

// The NewHandlerFuncs method implements the NewHandlerFuncs function. If the current HandlerExtender cannot create HandlerFuncs, it calls the superior HandlerExtender to process.
//
// NewHandlerFuncs 方法实现NewHandlerFuncs函数，如果当前HandlerExtender无法创建HandlerFuncs，则调用上级HandlerExtender处理。
func (ext *handlerExtendWarp) NewHandlerFuncs(path string, i interface{}) HandlerFuncs {
	hs := ext.HandlerExtender.NewHandlerFuncs(path, i)
	if hs != nil {
		return hs
	}
	return ext.LastExtender.NewHandlerFuncs(path, i)
}

// ListExtendHandlerNames 方法返回全部注册的函数名称。
func (ext *handlerExtendWarp) ListExtendHandlerNames() []string {
	return append(ext.LastExtender.ListExtendHandlerNames(), ext.HandlerExtender.ListExtendHandlerNames()...)
}

// NewHandlerExtendTree function creates a path-based function extender.
//
// Mainly implement path matching. All actions are processed by the node's HandlerExtender, and the NewHandlerExtendBase () object is used.
//
// All registration and creation actions will be performed by matching the lowest node of the tree. If it cannot be created, the tree nodes will be processed upwards in order.
//
// The NewHandlerExtendTree().RegisterHandlerExtend method registers a handler function based on the path, and initializes to NewHandlerExtendBase () if the HandlerExtender is empty.
//
// The NewHandlerExtendTree().NewHandlerFuncs method matches the child nodes of the tree based on the path, and then executes the NewHandlerFuncs method from the most child node up. If it returns non-null, it returns directly.
//
// NewHandlerExtendTree 函数创建一个基于路径的函数扩展者。
//
// 主要实现路径匹配，所有行为使用节点的HandlerExtender处理，使用NewHandlerExtendBase()对象。
//
// 所有注册和创建行为都会匹配树最下级节点执行，如果无法创建则在树节点依次向上处理。
//
// NewHandlerExtendTree().RegisterHandlerExtend 方法基于路径注册一个处理函数，如果HandlerExtender为空则初始化为NewHandlerExtendBase()。
//
// NewHandlerExtendTree().NewHandlerFuncs 方法基于路径向树子节点匹配，后从最子节点依次向上执行NewHandlerFuncs方法，如果返回非空直接返回，否在会依次执行注册行为。
func NewHandlerExtendTree() HandlerExtender {
	return &handlerExtendTree{}
}

// RegisterHandlerExtend 方法基于路径注册一个扩展函数。
func (ext *handlerExtendTree) RegisterHandlerExtend(path string, i interface{}) error {
	// 匹配当前节点注册
	if path == "" {
		if ext.HandlerExtender == nil {
			ext.HandlerExtender = NewHandlerExtendBase()
		}
		return ext.HandlerExtender.RegisterHandlerExtend("", i)
	}

	// 寻找对应的子节点注册
	for pos := range ext.childs {
		subStr, find := getSubsetPrefix(path, ext.childs[pos].path)
		if find {
			if subStr != ext.childs[pos].path {
				ext.childs[pos].path = strings.TrimPrefix(ext.childs[pos].path, subStr)
				ext.childs[pos] = &handlerExtendTree{
					path:   subStr,
					childs: []*handlerExtendTree{ext.childs[pos]},
				}
			}
			return ext.childs[pos].RegisterHandlerExtend(strings.TrimPrefix(path, subStr), i)
		}
	}

	// 追加一个新的子节点
	newnode := &handlerExtendTree{
		path:            path,
		HandlerExtender: NewHandlerExtendBase(),
	}
	ext.childs = append(ext.childs, newnode)
	return newnode.HandlerExtender.RegisterHandlerExtend(path, i)
}

// NewHandlerFuncs 函数基于路径创建多个对象处理函数。
//
// 递归依次寻找子节点，然后返回时创建多个对象处理函数，如果子节点返回不为空就直接返回。
func (ext *handlerExtendTree) NewHandlerFuncs(path string, data interface{}) HandlerFuncs {
	for _, child := range ext.childs {
		if strings.HasPrefix(path, child.path) {
			hs := child.NewHandlerFuncs(path[len(child.path):], data)
			if hs != nil {
				return hs
			}
			break
		}
	}

	if ext.HandlerExtender != nil {
		return ext.HandlerExtender.NewHandlerFuncs(path, data)
	}
	return nil
}

// listExtendHandlerNamesByPrefix 方法递归添加路径前缀返回扩展函数名称。
func (ext *handlerExtendTree) listExtendHandlerNamesByPrefix(prefix string) []string {
	prefix += ext.path
	var names []string
	if ext.HandlerExtender != nil {
		names = ext.HandlerExtender.ListExtendHandlerNames()
		if prefix != "" {
			for i := range names {
				names[i] = prefix + " " + names[i]
			}
		}
	}

	for i := range ext.childs {
		names = append(names, ext.childs[i].listExtendHandlerNamesByPrefix(prefix)...)
	}
	return names
}

// ListExtendHandlerNames 方法返回全部注册的函数名称。
func (ext *handlerExtendTree) ListExtendHandlerNames() []string {
	return ext.listExtendHandlerNamesByPrefix("")
}

// NewHandlerFuncsFilter 函数过滤掉多个请求上下文处理函数中的空对象。
func NewHandlerFuncsFilter(hs HandlerFuncs) HandlerFuncs {
	var num int
	for _, h := range hs {
		if h != nil {
			num++
		}
	}
	if num == len(hs) {
		return hs
	}

	// 返回新过滤空的处理函数。
	nhs := make(HandlerFuncs, 0, num)
	for _, h := range hs {
		if h != nil {
			nhs = append(nhs, h)
		}
	}
	return nhs
}

// NewHandlerFuncsCombine function merges two HandlerFuncs into one. The default maximum length is now 63, which exceeds panic.
//
// Used to reconstruct the slice and prevent the appended data from being confused.
//
// HandlerFuncsCombine 函数将两个HandlerFuncs合并成一个，默认现在最大长度63，超过过panic。
//
// 用于重构切片，防止切片append数据混乱。
func NewHandlerFuncsCombine(hs1, hs2 HandlerFuncs) HandlerFuncs {
	// if nil
	if len(hs1) == 0 {
		return hs2
	}
	if len(hs2) == 0 {
		return hs1
	}
	// combine
	finalSize := len(hs1) + len(hs2)
	if finalSize >= 127 {
		panic("HandlerFuncsCombine: too many handlers")
	}
	hs := make(HandlerFuncs, finalSize)
	copy(hs, hs1)
	copy(hs[len(hs1):], hs2)
	return hs
}

type reflectValue struct {
	_    *uintptr
	ptr  uintptr
	flag uintptr
}

// getFuncPointer 函数获取一个reflect值的地址作为唯一标识id。
func getFuncPointer(iValue reflect.Value) uintptr {
	val := *(*reflectValue)(unsafe.Pointer(&iValue))
	return val.ptr
}

// SetHandlerAliasName 函数设置一个函数处理对象原始名称，如果扩展未生成名称，使用此值。
//
// 在handlerExtendBase对象和ControllerInjectSingleton函数中使用到，用于传递控制器函数名称。
func SetHandlerAliasName(i interface{}, name string) {
	if name == "" {
		return
	}
	iValue, ok := i.(reflect.Value)
	if !ok {
		iValue = reflect.ValueOf(i)
	}
	val := *(*reflectValue)(unsafe.Pointer(&iValue))
	names := contextAliasName[val.ptr]
	index := int(val.flag >> 10)
	if len(names) <= index {
		newnames := make([]string, index+1)
		copy(newnames, names)
		names = newnames
		contextAliasName[val.ptr] = names
	}
	names[index] = name
}

func getHandlerAliasName(iValue reflect.Value) string {
	val := *(*reflectValue)(unsafe.Pointer(&iValue))
	names := contextAliasName[val.ptr]
	index := int(val.flag >> 10)
	if len(names) > index {
		return names[index]
	}
	return ""
}

// SetHandlerFuncName function sets the name of a request context handler.
//
// Note: functions are not comparable, the method names of objects are overwritten by other method names.
//
// SetHandlerFuncName 函数设置一个请求上下文处理函数的名称。
//
// 注意：函数不具有可比性，对象的方法的名称会被其他方法名称覆盖。
func SetHandlerFuncName(i HandlerFunc, name string) {
	if name == "" {
		return
	}
	contextSaveName[getFuncPointer(reflect.ValueOf(i))] = name
}

// String method implements the fmt.Stringer interface and implements the output function name.
//
// String 方法实现fmt.Stringer接口，实现输出函数名称。
func (h HandlerFunc) String() string {
	rh := reflect.ValueOf(h)
	ptr := getFuncPointer(rh)
	name, ok := contextFuncName[ptr]
	if ok {
		return name
	}
	name, ok = contextSaveName[ptr]
	if ok {
		return name
	}
	return runtime.FuncForPC(rh.Pointer()).Name()
}

// NewExtendHandlerHTTP 函数handlerHTTP接口转换成HandlerFunc。
func NewExtendHandlerHTTP(h handlerHTTP) HandlerFunc {
	return h.HandleHTTP
}

// NewExtendHandlerNetHTTP 函数转换处理http.Handler对象。
func NewExtendHandlerNetHTTP(h http.Handler) HandlerFunc {
	clone, ok := h.(interface{ CloneHandler() http.Handler })
	if ok {
		h = clone.CloneHandler()
	}
	return func(ctx Context) {
		h.ServeHTTP(ctx.Response(), ctx.Request())
	}
}

// NewExtendFuncNetHTTP1 函数转换处理func(http.ResponseWriter, *http.Request)类型。
func NewExtendFuncNetHTTP1(fn func(http.ResponseWriter, *http.Request)) HandlerFunc {
	return func(ctx Context) {
		fn(ctx.Response(), ctx.Request())
	}
}

// NewExtendFuncNetHTTP2 函数转换处理http.HandlerFunc类型。
func NewExtendFuncNetHTTP2(fn http.HandlerFunc) HandlerFunc {
	return func(ctx Context) {
		fn(ctx.Response(), ctx.Request())
	}
}

// NewExtendFunc 函数处理func()。
func NewExtendFunc(fn func()) HandlerFunc {
	return func(Context) {
		fn()
	}
}

func getFileLineFieldsVals(iValue reflect.Value) []interface{} {
	file, line := runtime.FuncForPC(iValue.Pointer()).FileLine(1)
	return []interface{}{file, line}
}

// NewExtendFuncRender 函数处理func() interface{}。
func NewExtendFuncRender(fn func() interface{}) HandlerFunc {
	fineLineFieldsVals := getFileLineFieldsVals(reflect.ValueOf(fn))
	return func(ctx Context) {
		data := fn()
		if ctx.Response().Size() == 0 {
			err := ctx.Render(data)
			if err != nil {
				ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
			}
		}
	}
}

// NewExtendFuncError 函数处理func() error返回的error处理。
func NewExtendFuncError(fn func() error) HandlerFunc {
	fineLineFieldsVals := getFileLineFieldsVals(reflect.ValueOf(fn))
	return func(ctx Context) {
		err := fn()
		if err != nil {
			ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
		}
	}
}

// NewExtendFuncRenderError 函数处理func() (interface{}, error)返回数据渲染和error处理。
func NewExtendFuncRenderError(fn func() (interface{}, error)) HandlerFunc {
	fineLineFieldsVals := getFileLineFieldsVals(reflect.ValueOf(fn))
	return func(ctx Context) {
		data, err := fn()
		if err == nil && ctx.Response().Size() == 0 {
			err = ctx.Render(data)
		}
		if err != nil {
			ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
		}
	}
}

// NewExtendFuncContextError 函数处理func(Context) error返回的error处理。
func NewExtendFuncContextError(fn func(Context) error) HandlerFunc {
	fineLineFieldsVals := getFileLineFieldsVals(reflect.ValueOf(fn))
	return func(ctx Context) {
		err := fn(ctx)
		if err != nil {
			ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
		}
	}
}

// NewExtendFuncContextRender 函数处理func(Context) interface{}返回数据渲染。
func NewExtendFuncContextRender(fn func(Context) interface{}) HandlerFunc {
	fineLineFieldsVals := getFileLineFieldsVals(reflect.ValueOf(fn))
	return func(ctx Context) {
		data := fn(ctx)
		if ctx.Response().Size() == 0 {
			err := ctx.Render(data)
			if err != nil {
				ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
			}
		}
	}
}

// NewExtendFuncContextRenderError 函数处理func(Context) (interface{}, error)返回数据渲染和error处理。
func NewExtendFuncContextRenderError(fn func(Context) (interface{}, error)) HandlerFunc {
	fineLineFieldsVals := getFileLineFieldsVals(reflect.ValueOf(fn))
	return func(ctx Context) {
		data, err := fn(ctx)
		if err == nil && ctx.Response().Size() == 0 {
			err = ctx.Render(data)
		}
		if err != nil {
			ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
		}
	}
}

// NewExtendFuncContextInterfaceError 函数处理func(Context) (T, error)返回数据渲染和error处理。
func NewExtendFuncContextInterfaceError(fn interface{}) HandlerFunc {
	iValue := reflect.ValueOf(fn)
	iType := iValue.Type()
	if iType.Kind() != reflect.Func || iType.NumIn() != 1 || iType.NumOut() != 2 || iType.In(0) != typeContext || iType.Out(1) != typeError {
		return nil
	}

	fineLineFieldsVals := getFileLineFieldsVals(reflect.ValueOf(fn))
	return func(ctx Context) {
		vals := iValue.Call([]reflect.Value{reflect.ValueOf(ctx)})
		err, _ := vals[1].Interface().(error)
		if err == nil && ctx.Response().Size() == 0 {
			err = ctx.Render(vals[0].Interface())
		}
		if err != nil {
			ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
		}
	}
}

// NewExtendFuncRPCMap defines a fixed request and response to function processing of type map [string] interface {}.
//
// is a subset of NewRPCHandlerFunc and has type restrictions, but using map [string] interface {} to save requests does not use reflection.
//
// NewExtendFuncRPCMap 定义了固定请求和响应为map[string]interface{}类型的函数处理。
//
// 是NewRPCHandlerFunc的一种子集，拥有类型限制，但是使用map[string]interface{}保存请求没用使用反射。
func NewExtendFuncRPCMap(fn func(Context, map[string]interface{}) (interface{}, error)) HandlerFunc {
	fineLineFieldsVals := getFileLineFieldsVals(reflect.ValueOf(fn))
	return func(ctx Context) {
		req := make(map[string]interface{})
		err := ctx.Bind(&req)
		if err != nil {
			ctx.Fatal(err)
			return
		}
		resp, err := fn(ctx, req)
		if err == nil && ctx.Response().Size() == 0 {
			err = ctx.Render(resp)
		}
		if err != nil {
			ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
		}
	}
}

// NewExtendHandlerRPC function needs to pass in a function that returns a request for processing and is dynamically called by reflection.
//
// Function form: func (Context, Request) (Response, error)
//
// The types of Request and Response can be map or struct or pointer to struct. All 4 parameters need to exist, and the order cannot be changed.
//
// NewExtendHandlerRPC 函数需要传入一个函数，返回一个请求处理，通过反射来动态调用。
//
// 函数形式： func(Context, Request) (Response, error)
//
// Request和Response的类型可以为map或结构体或者结构体的指针，4个参数需要全部存在，且不可调换顺序。
func NewExtendHandlerRPC(fn interface{}) HandlerFunc {
	iType := reflect.TypeOf(fn)
	iValue := reflect.ValueOf(fn)
	if iType.Kind() != reflect.Func {
		return nil
	}
	if iType.NumIn() != 2 || iType.In(0) != typeContext {
		return nil
	}
	if iType.NumOut() != 2 || iType.Out(1) != typeError {
		return nil
	}
	var typeIn = iType.In(1)
	var kindIn = typeIn.Kind()
	var typenew = iType.In(1)
	// 检查请求类型
	switch typeIn.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Struct:
	default:
		return nil
	}
	if typenew.Kind() == reflect.Ptr {
		typenew = typenew.Elem()
	}

	fineLineFieldsVals := getFileLineFieldsVals(iValue)
	return func(ctx Context) {
		// 创建请求参数并初始化
		req := reflect.New(typenew)
		err := ctx.Bind(req.Interface())
		if err != nil {
			ctx.Fatal(err)
			return
		}
		if kindIn != reflect.Ptr {
			req = req.Elem()
		}

		// 反射调用执行函数。
		vals := iValue.Call([]reflect.Value{reflect.ValueOf(ctx), req})

		// 检查函数执行err。
		err, ok := vals[1].Interface().(error)
		if ok {
			ctx.WithFields(fineLineFieldsKeys, fineLineFieldsVals).Fatal(err)
			return
		}

		// 渲染返回的数据。
		err = ctx.Render(vals[0].Interface())
		if err != nil {
			ctx.Fatal(err)
		}
	}
}

// NewExtendHandlerStringer 函数处理fmt.Stringer接口类型转换成HandlerFunc。
func NewExtendHandlerStringer(fn fmt.Stringer) HandlerFunc {
	return func(ctx Context) {
		ctx.WriteString(fn.String())
	}
}

// NewExtendFuncString 函数处理func() string，然后指定函数生成的字符串。
func NewExtendFuncString(fn func() string) HandlerFunc {
	return func(ctx Context) {
		ctx.WriteString(fn())
	}
}

// NewStaticHandler 函数更加目标创建一个静态文件处理函数。
//
// 参数dir指导打开文件的根目录，默认未"."
//
// 路由规则可以指导path参数为请求文件路径，例如/static/*path，将会去打开path参数路径的文件，否在使用ctx.Path().
func NewStaticHandler(name, dir string) HandlerFunc {
	if name == "" {
		name = "*"
	}
	if dir == "" {
		dir = "."
	}
	return func(ctx Context) {
		path := ctx.GetParam(name)
		if path == "" {
			path = ctx.Path()
		}
		if ctx.Request().Header.Get(HeaderCacheControl) == "" {
			ctx.SetHeader(HeaderCacheControl, "no-cache")
		}
		ctx.WriteFile(filepath.Join(dir, filepath.Clean("/"+path)))
	}
}

// HandlerEmpty 函数定义一个空的请求上下文处理函数。
func HandlerEmpty(Context) {
	// Do nothing because empty handler does not process entries.
}
